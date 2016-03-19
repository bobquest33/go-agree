//Package agree helps you distribute any data structure using Raft.
package agree

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/raft"
	"github.com/hashicorp/raft-boltdb"
	"net"
	"net/rpc"
	"net/rpc/jsonrpc"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"
)

var (
	//ErrMethodNotFound is the error that is returned if you try to apply a method that the type does not have.
	ErrMethodNotFound = errors.New("Cannot apply the method as it was not found")

	//RaftDirectory is the directory where raft files should be stored.
	RaftDirectory = "."

	//RetainSnapshotCount is the number of Raft snapsnots that will be retained.
	RetainSnapshotCount = 2
)

type agreeable interface {
	Mutate(method string, args ...interface{}) error
	Set(interface{}) error
	Marshal() ([]byte, error)
}

type Config struct {
	Peers      []string
	RaftConfig *raft.Config
	RPCPort    string
	RaftBind   string
}

//Mutation is passed to observers to notify them of mutations. Observers should not
//mutate NewValue.
type Mutation struct {
	NewValue   interface{}   // The new, mutated wrapped value
	Method     string        // The name of the method passed to Mutate()
	MethodArgs []interface{} // The arguments the method was called with
}

type Callback func(args Mutation)

//Wrapper is a wrapper for the datastructure you want to distribute.
//It inherics from sync/RWMutex and you should use RLock()/RUnlock() when calling read-only methods.
type Wrapper struct {
	sync.RWMutex
	raftClient    *raft.Raft
	value         interface{}
	fsm           *fsm
	callbacks     map[string][]Callback
	callbackChans map[string][]chan *Mutation
	reflectVal    reflect.Value
	reflectType   reflect.Type
	methods       map[string]reflect.Value
	config        *Config
}

func (t *Wrapper) Marshal() ([]byte, error) {
	t.RLock()
	defer t.RUnlock()
	return json.Marshal(t.value)
}

func (t *Wrapper) startRaft(c *Config) (*raft.Raft, error) {
	var config *raft.Config
	// Setup Raft configuration.
	if c.RaftConfig == nil {
		config = raft.DefaultConfig()

		// Allow the node to entry single-mode, potentially electing itself, if
		// explicitly enabled and there is only 1 node in the cluster already.
		if len(c.Peers) == 0 {
			config.EnableSingleNode = true
			config.DisableBootstrapAfterElect = false
		}

	}

	// Setup Raft communication.
	addr, err := net.ResolveTCPAddr("tcp", c.RaftBind)
	if err != nil {
		return nil, err
	}
	transport, err := raft.NewTCPTransport(c.RaftBind, addr, 3, 10*time.Second, os.Stderr)
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
	ra, err := raft.NewRaft(config, t.fsm, logStore, logStore, snapshots, peerStore, transport)
	if err != nil {
		return nil, fmt.Errorf("new raft: %s", err)
	}

	//block until a leader is elected
	for ra.Leader() == "" {
		time.Sleep(time.Second)
	}
	return ra, nil
}

//Wrap returns a wrapper for your type. Type methods should have JSON-marshallable arguments.
func Wrap(i interface{}, c *Config) (*Wrapper, error) {

	if c.RaftBind == "" {
		c.RaftBind = "127.0.0.1:8080"
	}

	if c.RPCPort == "" {
		c.RPCPort = "8081"
	}

	methods := make(map[string]reflect.Value)
	t := reflect.TypeOf(i)
	v := reflect.ValueOf(i)
	for j := 0; j < t.NumMethod(); j++ {
		method := t.Method(j)
		methods[method.Name] = v.MethodByName(method.Name)
	}
	ret := Wrapper{
		config:      c,
		value:       i,
		reflectVal:  v,
		reflectType: t,
		methods:     methods,
		callbacks:   make(map[string][]Callback),
	}

	ret.fsm = &fsm{
		config:     c,
		underlying: &ret,
	}

	r, err := ret.startRaft(c)

	if err != nil {
		return nil, err
	}

	ret.fsm.raft = r

	ret.fsm.fsmRPC = &ForwardingClient{fsm: ret.fsm}

	rpc.Register(ret.fsm.fsmRPC)
	rpc.HandleHTTP()
	l, err := net.Listen("tcp", ":"+c.RPCPort)

	if err != nil {
		return nil, err
	}

	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				continue
			}
			go jsonrpc.ServeConn(c)
		}

	}()

	return &ret, nil
}

func (t *Wrapper) forwardCommandToLeader(method string, args ...interface{}) error {
	leader := t.fsm.raft.Leader()
	client, err := jsonrpc.Dial("tcp", leader+":"+t.config.RPCPort)
	if err != nil {
		return err
	}

	arg := LogEntry{
		Method: method,
		Args:   args,
	}

	var b []byte
	b, err = json.Marshal(arg)

	if err != nil {
		return err
	}

	var reply interface{}

	err = client.Call("ForwardingClient.Apply", b, &reply)

	return err
}

func (t *Wrapper) forwardAddNodeToLeader(addr string) error {
	leader := t.fsm.raft.Leader()
	client, err := jsonrpc.Dial("tcp", leader+":"+t.config.RPCPort)
	if err != nil {
		return err
	}
	if err != nil {
		return err
	}
	var reply struct{}

	err = client.Call("ForwardingClient.AddNode", addr, &reply)

	return err
}

func (t *Wrapper) forwardRemoveNodeToLeader(addr string) error {
	leader := t.fsm.raft.Leader()
	client, err := jsonrpc.Dial("tcp", leader+":"+t.config.RPCPort)
	if err != nil {
		return err
	}
	if err != nil {
		return err
	}
	var reply struct{}

	err = client.Call("ForwardingClient.RemoveNode", addr, &reply)

	return err
}

//Mutate performs an operation that mutates your data.
func (t *Wrapper) Mutate(method string, args ...interface{}) error {

	if t.fsm.raft.State() != raft.Leader {
		return t.forwardCommandToLeader(method, args)
	}

	var cmd = LogEntry{
		Method: method,
		Args:   args,
	}

	b, err := json.Marshal(cmd)

	if err != nil {
		return err
	}

	t.fsm.raft.Apply(b, raftTimeout)

	return nil

}

//AddNode adds a node, located at addr, to the cluster. The node must be ready to respond to Raft
//commands at the address.
func (t *Wrapper) AddNode(addr string) error {
	if t.fsm.raft.State() != raft.Leader {
		return t.forwardAddNodeToLeader(addr)
	}

	f := t.fsm.raft.AddPeer(addr)
	if f.Error() != nil {
		return f.Error()
	}
	return nil
}

//RemoveNode removes a node, located at addr, from the cluster.
func (t *Wrapper) RemoveNode(addr string) error {
	if t.fsm.raft.State() != raft.Leader {
		return t.forwardRemoveNodeToLeader(addr)
	}

	f := t.fsm.raft.RemovePeer(addr)
	if f.Error() != nil {
		return f.Error()
	}
	return nil
}

//SubscribeFunc executes the `notify` func when the distributed object is mutated by applying `Mutate` on `method`.
//The first parameter the notify func receives is the data structure and the variadic args are a copy
//of the arguments passed to Mutate.
//The func should not mutate the interface or strange things will happen.
func (t *Wrapper) SubscribeFunc(method string, notify Callback) {
	t.callbacks[method] = append(t.callbacks[method], notify)
}

//SubscribeChan sends values to the returned channel when the underlying structure is mutated. The
//values are a slice of interface{}: the first entry is the structure and the next entries are the
//arguments passed to the method. For example, calling Mutate("method", "arg1" ,"arg2") will result
//in a 3-element slice being passed to the channel - the first will be the wrapped interface{},
//and the second and third elements will be "arg1" and "arg2", respectively.
func (t *Wrapper) SubscribeChan(method string, c chan *Mutation) {
	t.callbackChans[method] = append(t.callbackChans[method], c)
}

//Inspect gives f access to the distributed data. While f is executing, no goroutine may mutate
//the data. Multiple goroutines can call Inspect concurrently.
//f should not mutate the value or strange things will happen.
func (t *Wrapper) Inspect(f func(interface{})) {
	t.RLock()
	defer t.RUnlock()
	f(t.value)
}
