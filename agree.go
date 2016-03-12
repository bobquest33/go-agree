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
	"net/rpc"
	"net/rpc/jsonrpc"
)

var (
	//ErrMethodNotFound is the error that is returned if you try to apply a method that the type does not have.
	ErrMethodNotFound = errors.New("Cannot apply the method as it was not found")
	
	//RaftDirectory is the directory where raft files should be stored.
	RaftDirectory = "."
	
	//RetainSnapshotCount is the number of Raft snapsnots that will be retained.
	RetainSnapshotCount = 2
)

type Config struct {
	Peers []string
	RaftConfig *raft.Config
	RPCPort string
	RaftBind string
}

type Callback func(interface{}, ...interface{})

//T is a wrapper for the datastructure you want to distribute. 
//It inherics from sync/RWMutex and you should use RLock()/RUnlock() when calling read-only methods.
type T struct {
	sync.RWMutex
	raftClient *client
	value interface{}
	fsm *fsm
	client *client
	callbacks map[string][]Callback
	reflectVal reflect.Value
	reflectType reflect.Type
	methods map[string]reflect.Value
	config *Config
}

type client struct {
	ra *raft.Raft
}

func (t *T) newClient(c *Config) (*client, error) {
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
	var ret client
	ret.ra = ra
	return &ret, nil	
}


//Wrap returns a wrapper for your type. Type methods should have JSON-marshallable arguments.
func Wrap(i interface{}, c *Config) (*T, error) {
	
	if c.RaftBind == "" {
		c.RaftBind = "127.0.0.1:8080"
	}
	
	if c.RPCPort == "" {
		c.RPCPort = "8081"
	}
	
	methods := make(map[string]reflect.Value)
	t := reflect.TypeOf(i)
	v := reflect.ValueOf(i)
	for j := 0; j< t.NumMethod(); j++{
		method := t.Method(j)
		methods[method.Name] = v.MethodByName(method.Name)
	}
	ret :=  T{
		config: c,
		value: i,
		reflectVal: v,
		reflectType: t,
		methods: methods,
		callbacks: make(map[string][]Callback),
	}
	

	
	ret.fsm = &fsm{
		config: c,
		underlying: &ret,
	}
	
	client, err := ret.newClient(c)
	
	if err != nil {
		return nil, err
	}
	
	ret.fsm.client = client

	ret.fsm.fsmRPC = &ForwardingClient{fsm: ret.fsm}
	
	rpc.Register(ret.fsm.fsmRPC)
	rpc.HandleHTTP()
	l, err := net.Listen("tcp", ":" + c.RPCPort)

	if err != nil {
		return nil, err
	}
	
	go func(){
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

func (t *T) mutateLeader(method string, args...interface{}) error {
	leader := t.fsm.client.ra.Leader()
	client, err := jsonrpc.Dial("tcp", leader + ":" + t.config.RPCPort)
	if err != nil {
		return err
	}
	
	arg := Command{
		Method: method,
		Args: args,
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

//Mutate performs an operation that mutates your data.
func (t *T) Mutate(method string, args...interface{}) error{
	
	if t.fsm.client.ra.State() != raft.Leader { 
		return t.mutateLeader(method, args)
	}
	
	var cmd = Command{
		Method: method,
		Args: args,
	}
	
	b, err := json.Marshal(cmd)
	
	if err != nil {
		return err
	}
	
	t.fsm.client.ra.Apply(b, raftTimeout)
	
	return nil
	
}

//Subscribe executes the `notify` func when the distributed object is mutated by applying `Mutate` on `method`.
//The first parameter the notify func receives is the data structure and the variadic args are a copy 
//of the arguments passed to Mutate.  
//The func should not mutate the interface or strange things will happen.
func (t *T) Subscribe(method string, notify Callback){
	t.callbacks[method] = append(t.callbacks[method], notify)
}

//Inspect gives f access to the distributed data. While f is executing, no goroutine may mutate 
//the data. Multiple goroutines can call Inspect concurrently.
//f should not mutate the value or strange things will happen.
func (t *T) Inspect(f func(interface{})){
	t.RLock()
	defer t.RUnlock()
	f(t.value)
}