package agree

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/raft"
	"io"
	"reflect"
	"time"
)

var (
	//ErrNotLeader is returned when a Command is mistakenly sent to a follower. You should never receive this as Go-Agree takes care of following commands to the leader.
	ErrNotLeader = errors.New("Commands should be sent to leader and cannot be sent to followers")

	//ErrIncorrectType is returned when a Raft snapshot cannot be unmarshalled to the expected type.
	ErrIncorrectType = errors.New("Snapshot contained data of an incorrect type")
	
	//ErrTooManyalues is returned when a Read() method returns more than one value (plus optional error return)
	ErrTooManyalues = errors.New("Method returned too many values")
)

const raftTimeout = time.Second * 10

//ForwardingClient is a client that forwards commands to the Raft leader. Should not be used,
//the only reason it is exported is because the rpc package requires it.
type ForwardingClient struct {
	fsm *fsm
}

type fsm struct {
	fsmRPC  *ForwardingClient
	config  *Config
	raft    *raft.Raft
	wrapper *Wrapper
}

//Command represents a mutating Command (log entry) in the Raft commit log.
type Command struct {
	Method string
	Args   []interface{}
}

type fsmSnapshot struct {
	store interface{}
}

//Apply forwards the given mutating Command to the Raft leader.
func (r *ForwardingClient) Apply(cmd []byte, reply *int) error {
	if r.fsm.raft.State() != raft.Leader {
		return ErrNotLeader
	}

	if errF := r.fsm.raft.Apply(cmd, raftTimeout); errF.Error() != nil {
		return errF.Error()
	}

	return nil
}

//AddPeer accepts a forwarded request to add a peer, sent to the Raft leader.
func (r *ForwardingClient) AddPeer(addr string, reply *int) error {
	if r.fsm.raft.State() != raft.Leader {
		return ErrNotLeader
	}
	return r.fsm.wrapper.AddNode(addr)
}

//RemovePeer accepts a forwarded request to remove a peer, sent to the Raft leader.
func (r ForwardingClient) RemovePeer(addr string, reply *int) error {
	if r.fsm.raft.State() != raft.Leader {
		return ErrNotLeader
	}
	return r.fsm.wrapper.RemoveNode(addr)
}

func (f *fsmSnapshot) Persist(sink raft.SnapshotSink) error {
	err := func() error {
		// Encode data.
		b, err := json.Marshal(f.store)
		if err != nil {
			return err
		}

		// Write data to sink.
		if _, err := sink.Write(b); err != nil {
			return err
		}

		// Close the sink.
		if err := sink.Close(); err != nil {
			return err
		}

		return nil
	}()

	if err != nil {
		sink.Cancel()
		return err
	}

	return nil
}

func (f *fsmSnapshot) Release() {}

func (f *fsm) Snapshot() (raft.FSMSnapshot, error) {

	data, err := f.wrapper.Marshal()

	if err != nil {
		return nil, err
	}

	return &fsmSnapshot{store: data}, nil

}

func (f *fsm) Restore(rc io.ReadCloser) error {
	var err error
	e := f.wrapper.reflectType.Elem()
	o := reflect.New(e).Interface()
	if err := json.NewDecoder(rc).Decode(&o); err != nil {
		return err
	}

	if !reflect.TypeOf(o).AssignableTo(f.wrapper.reflectType) {
		return ErrIncorrectType
	}

	// Set the state from the snapshot, no lock required according to
	// Hashicorp docs.
	f.wrapper.Lock()
	f.wrapper.value = o
	f.wrapper.Unlock()
	return err
}

func (f *fsm) Read(method string, args...interface{}) (interface{}, error){

	t := f.wrapper
	var (
		m reflect.Value
		found bool 
	)
	if m, found = t.methods[method]; !found {
		return nil, ErrMethodNotFound
	}
	//fmt.Println(cmd)

	var callArgs []reflect.Value

	for i := range args {
		callArgs = append(callArgs, reflect.ValueOf(args[i]))
	}

	f.wrapper.Lock()
	defer f.wrapper.Unlock()
	ret := m.Call(callArgs)
	
	switch len(ret){
		case 0:
			return nil, nil 
		case 1:
			return ret[0].Interface(), nil
		case 2:
			if err, ok := ret[1].Interface().(error); ok {
				return ret[0].Interface(), err 
			} else if !ok {
				return nil, errors.New("If returning two values the second value should be of error type")
			}
		default:
			return nil, ErrTooManyalues
	}

	//unreachable	
	return nil, nil

}

func (f *fsm) Apply(l *raft.Log) interface{} {
	var (
		cmd   Command
		m     reflect.Value
		found bool
	)
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		panic(fmt.Sprintf("Could not unmarshal Command: %s", err.Error()))
	}

	t := f.wrapper
	if m, found = t.methods[cmd.Method]; !found {
		return ErrMethodNotFound
	}
	//fmt.Println(cmd)

	var callArgs []reflect.Value

	for i := range cmd.Args {
		callArgs = append(callArgs, reflect.ValueOf(cmd.Args[i]))
	}

	f.wrapper.Lock()
	defer f.wrapper.Unlock()
	ret := m.Call(callArgs)

	for _, callback := range f.wrapper.callbacks[cmd.Method] {
		callback(Mutation{
			NewValue:   f.wrapper.value,
			Method:     cmd.Method,
			MethodArgs: cmd.Args,
		})
	}

	for _, c := range f.wrapper.callbackChans[cmd.Method] {
		c <- &Mutation{
			NewValue:   f.wrapper.value,
			Method:     cmd.Method,
			MethodArgs: cmd.Args,
		}
	}

	switch {
	case len(ret) == 0:
		return nil
	case len(ret) > 1:
		panic("Applied methods should have at most one return parameter, and it should satisfy error interface")
	default:
		//one return param, which should satisfy error interface
		return ret[0].Interface().(error)
	}

}
