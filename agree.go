//Package agree helps you distribute any data structure using Raft.
package agree

import (
	"reflect"
	"errors"
	"sync"
	"github.com/hashicorp/raft"
	"github.com/hashicorp/raft-boltdb"
	"net"
	"time"
	"os"
	"fmt"
	"path/filepath"
	"encoding/json"
	"io"
)

var (
	//ErrMethodNotFound is the error that is returned if you try to apply a method that the type does not have.
	ErrMethodNotFound = errors.New("Cannot apply the method as it was not found")
	
	//RaftDirectory is the directory where raft files should be stored.
	RaftDirectory = "."
	
	//RaftPort is the port we'll bind to for Raft communication.
	RaftPort = "8080"
	
	//RetainSnapshotCount is the number of Raft snapsnots that will be retained.
	RetainSnapshotCount = 2
)

type Callback func(interface{}, ...interface{})

type methodCall struct {
	Method string 
	Args []interface{}
}

type fsm struct {
	client *Client
	underlying *T 
}

type fsmSnapshot struct {
	store interface{}
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
	e := f.underlying.reflectType.Elem()
	o := reflect.New(e).Interface()
	if err := json.NewDecoder(rc).Decode(&o); err != nil {
		return err
	}

	// Set the state from the snapshot, no lock required according to
	// Hashicorp docs.
	f.underlying = f.client.Wrap(o)
	return nil
}

//T is a wrapper for the datastructure you want to distribute. 
//It inherics from sync/RWMutex and you should use RLock()/RUnlock() when calling read-only methods.
type T struct {
	sync.RWMutex
	raftClient *Client
	value interface{}
	fsm *fsm
	callbacks map[string][]Callback
	reflectVal reflect.Value
	reflectType reflect.Type
	methods map[string]reflect.Value
}

type Client struct {
	ra *raft.Raft
}

// Listen starts the Raft protocol. You must call this before Apply'ing anything.
func New(peers []string, config *raft.Config) (*Client, error){

	// Setup Raft configuration.
	if config == nil {
		config = raft.DefaultConfig()
		
		// Allow the node to entry single-mode, potentially electing itself, if
		// explicitly enabled and there is only 1 node in the cluster already.
		if len(peers) <= 1 {
			config.EnableSingleNode = true
			config.DisableBootstrapAfterElect = false
		}
	
	}
	

	// Setup Raft communication.
	addr, err := net.ResolveTCPAddr("tcp", RaftPort)
	if err != nil {
		return nil, err
	}
	transport, err := raft.NewTCPTransport(RaftPort, addr, 3, 10*time.Second, os.Stderr)
	if err != nil {
		return nil, err
	}

	// Create peer storage.
	peerStore := raft.NewJSONPeers(RaftDirectory, transport)

	// Create the snapshot store. This allows the Raft to truncate the log.
	snapshots, err := raft.NewFileSnapshotStore(RaftDirectory, RetainSnapshotCount, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("file snapshot store: %s", err)
	}

	// Create the log store and stable store.
	logStore, err := raftboltdb.NewBoltStore(filepath.Join(RaftDirectory, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("new bolt store: %s", err)
	}

	// Instantiate the Raft systems.
	//ra, err := raft.NewRaft(config, (*fsm)(s), logStore, logStore, snapshots, peerStore, transport)
	
	//TODO: Implement FSM interface
	ra, err := raft.NewRaft(config,nil, logStore, logStore, snapshots, peerStore, transport)
	if err != nil {
		return nil, fmt.Errorf("new raft: %s", err)
	}
	var client Client
	client.ra = ra
	return &client, nil	
}


//Wrap returns a wrapper for your type. Type methods should have JSON-marshallable arguments.
func (c *Client) Wrap(i interface{}) *T{
	methods := make(map[string]reflect.Value)
	t := reflect.TypeOf(i)
	v := reflect.ValueOf(i)
	for j := 0; j< t.NumMethod(); j++{
		method := t.Method(j)
		methods[method.Name] = v.MethodByName(method.Name)
	}
	ret :=  T{
		value: i,
		reflectVal: v,
		reflectType: t,
		methods: methods,
		raftClient: c,
		callbacks: make(map[string][]Callback),
	}
	
	ret.fsm = &fsm{
		client: c,
		underlying: &ret,
	}
	
	return &ret
}

//Mutate performs an operation that mutates your data.
func (t *T) Mutate(method string, args...interface{}) error{
	var (
		m reflect.Value
		found bool
	)
	
	if m, found  = t.methods[method]; !found {
		return ErrMethodNotFound
	}
	
	var callArgs []reflect.Value
	
	for i := range args {
		callArgs = append(callArgs, reflect.ValueOf(args[i]))
	}
	
	t.Lock()
	defer t.Unlock()
	
	ret := m.Call(callArgs)
	
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

//When executes the `notify` func when the distributed object is mutated by applying `Mutate` on `method`.
//The first parameter the notify func receives is the data structure and the variadic args are a copy 
//of the arguments passed to Mutate.  
//The func should not mutate the interface or strange things will happen.
func (t *T) When(method string, notify Callback){
	t.callbacks[method] = append(t.callbacks[method], notify)
}

//Copy returns a deep copy of the data contained in t. You should avoid using this often - 
//it is better to use When() to react to data changes.
func (t *T) Copy() (interface{}, error){
	
	val, err := json.Marshal(t.value)
	
	if err != nil {
		return nil, err
	}
	
	var i interface{}

	err = json.Unmarshal(val, &i)
	
	return i, err
}