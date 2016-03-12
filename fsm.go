package agree

import (
	"io"
	"github.com/hashicorp/raft"
	"encoding/json"
	"reflect"
	"errors"
	"fmt"
	"time"
)

var ErrNotLeader = errors.New("Commands should be sent to leader and cannot be sent to followers")

const raftTimeout = time.Second * 10

type ForwardingClient struct {
	fsm *fsm
}

type fsm struct {
	fsmRPC *ForwardingClient
	config *Config
	client *client
	underlying *T 
}

type Command struct {
	Method string 
	Args []interface{}
}

type JSONCommand []byte

var ErrIncorrectType = errors.New("Snapshot contained data of an incorrect type")

type fsmSnapshot struct {
	store interface{}
}

func (r *ForwardingClient) Apply(cmd JSONCommand, reply *interface{}) error {
	if r.fsm.client.ra.State() != raft.Leader {
		return ErrNotLeader
	}	
	
	
	if errF := r.fsm.client.ra.Apply(cmd, raftTimeout); errF != nil {
		return errF.(error)
	}
	return nil
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


func (f *fsmSnapshot) Release(){}

func (f *fsm) Snapshot() (raft.FSMSnapshot, error){
	f.underlying.Lock()
	defer f.underlying.Unlock()
	
	data, err := f.underlying.Copy()
	
	if err != nil {
		return nil, err
	}
	
	return &fsmSnapshot{store: data}, nil
	
}

func (f *fsm) Restore(rc io.ReadCloser) error {
	var err error
	e := f.underlying.reflectType.Elem()
	o := reflect.New(e).Interface()
	if err := json.NewDecoder(rc).Decode(&o); err != nil {
		return err
	}
	
	if !reflect.TypeOf(o).AssignableTo(f.underlying.reflectType){
		return ErrIncorrectType
	}

	// Set the state from the snapshot, no lock required according to
	// Hashicorp docs.
	f.underlying.Lock()
	f.underlying.value = o
	f.underlying.Unlock()
	return err
}

func (f *fsm) Apply(l *raft.Log) interface{} {
	var cmd Command
	if err := json.Unmarshal(l.Data, &cmd); err != nil {
		panic(fmt.Sprintf("Could not unmarshal command: %s", err.Error()))
	}
	
	if err := f.underlying.Mutate(cmd.Method, cmd.Args...); err != nil {
		panic(fmt.Sprintf("Could not apply command %s: %s", cmd.Method, err.Error()))
	}
	
	return nil	
}