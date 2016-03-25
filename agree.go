//Package agree helps you distribute any data structure using Raft.
package agree

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hashicorp/raft"
	"github.com/hashicorp/raft-boltdb"
	"io/ioutil"
	"net"
	"net/http"
	"net/rpc"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	//ErrMethodNotFound is the error that is returned if you try to apply a method that the type does not have.
	ErrMethodNotFound = errors.New("Cannot apply the method as it was not found")

	//DefaultRaftDirectory is the default directory where raft files should be stored.
	DefaultRaftDirectory = "."

	//DefaultRetainSnapshotCount is the number of Raft snapsnots that will be retained.
	DefaultRetainSnapshotCount = 2
)

//Config is a configuration struct that is passed to Wrap(). It specifies Raft settings and command forwarding port.
type Config struct {
	Peers               []string     // List of peers. Peers' raft ports can be different but the forwarding port must be the same for each peer in the cluster.
	RaftConfig          *raft.Config // Raft configuration, see github.com/hashicorp/raft. Default raft.DefaultConfig()
	forwardingBind      string       //Where forwarding client binds. Hardcoded to raft port + 1 for now.
	RaftBind            string       // Where to bind Raft, default ":8080"
	RaftDirectory       string       // Where Raft files will be stored
	RetainSnapshotCount int          // How many Raft snapshots to retain
}

//Mutation is passed to observers to notify them of mutations. Observers should not
//mutate NewValue.
type Mutation struct {
	NewValue   interface{}   // The new, mutated wrapped value
	Method     string        // The name of the method passed to Mutate()
	MethodArgs []interface{} // The arguments the method was called with
}

//Callback is a callback function that is invoked when you subscribe to mutations using Wrapper.SusbcribeFunc() and a mutation occurs.
//The args contain the details of the mutation that just occurred.
type Callback func(m Mutation)

//Wrapper is a wrapper for the datastructure you want to distribute.
//It inherics from sync/RWMutex and if you retained a pointer to the interface before you passed it to
//Wrap(), you should RLock()/RUnlock() the wrapper whenever you access the interface's value outside Go-Agree's helper
//methods.
type Wrapper struct {
	sync.RWMutex
	value         interface{}
	fsm           *fsm
	callbacks     map[string][]Callback
	callbackChans map[string][]chan *Mutation
	reflectVal    reflect.Value
	reflectType   reflect.Type
	methods       map[string]reflect.Value
	config        *Config
}

//Marshal marshals the wrapper's value using encoding/json.
func (w *Wrapper) Marshal() ([]byte, error) {
	w.RLock()
	defer w.RUnlock()
	return json.Marshal(w.value)
}

func (w *Wrapper) startRaft(c *Config) (*raft.Raft, error) {
	var config *raft.Config

	if c.RaftDirectory == "" {
		c.RaftDirectory = DefaultRaftDirectory
	}
	subdir := strings.Replace(w.reflectType.String(), "*", "", 2)
	
	if err := os.Mkdir(subdir, os.ModePerm); err != nil && !strings.Contains(err.Error(), "file exists") {
		return nil, err
	}
	
	
	raftDirectory := filepath.Join(c.RaftDirectory, subdir)
	if c.RetainSnapshotCount == 0 {
		c.RetainSnapshotCount = DefaultRetainSnapshotCount
	}

	// Check for any existing peers.
	peers, err := readPeersJSON(filepath.Join(raftDirectory, "peers.json"))
	if err != nil {
		return nil, err
	}
	// Setup Raft configuration.
	if c.RaftConfig == nil {
		config = raft.DefaultConfig()

		// Allow the node to entry single-mode, potentially electing itself, if
		// explicitly enabled and there is only 1 node in the cluster already.
		if len(peers) <= 1 && len(c.Peers) == 0 {
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
	peerStore := raft.NewJSONPeers(raftDirectory, transport)

	if len(c.Peers) > 0 {
		if err := peerStore.SetPeers(c.Peers); err != nil {
			return nil, fmt.Errorf("error setting peers: %s", err)
		}
	}

	// Create the snapshot store. This allows the Raft to truncate the log.
	snapshots, err := raft.NewFileSnapshotStore(raftDirectory, c.RetainSnapshotCount, os.Stderr)
	if err != nil {
		return nil, fmt.Errorf("file snapshot store: %s", err)
	}

	// Create the log store and stable store.
	logStore, err := raftboltdb.NewBoltStore(filepath.Join(raftDirectory, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("new bolt store: %s", err)
	}

	ra, err := raft.NewRaft(config, w.fsm, logStore, logStore, snapshots, peerStore, transport)
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
		c.RaftBind = ":8080"
	}

	forwardingAddr, err := incrementPort(c.RaftBind, 1)
	if err != nil {
		return nil, err
	}
	c.forwardingBind = forwardingAddr

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
		config:  c,
		wrapper: &ret,
	}

	r, err := ret.startRaft(c)

	if err != nil {
		return nil, err
	}

	ret.fsm.raft = r

	ret.fsm.fsmRPC = &ForwardingClient{fsm: ret.fsm}

	rpc.Register(ret.fsm.fsmRPC)
	rpc.HandleHTTP()
	l, err := net.Listen("tcp", c.forwardingBind)

	if err != nil {
		return nil, err
	}

	go http.Serve(l, nil)

	return &ret, nil
}

func (w *Wrapper) forwardToLeader(rpcMethod string, request interface{}) error {
	leader := w.fsm.raft.Leader()
	leaderForwardingAddr, err := incrementPort(leader, 1)
	if err != nil {
		return err
	}
	client, err := rpc.DialHTTP("tcp", leaderForwardingAddr)
	
	if err != nil {
		return err
	}

	var reply int

	err = client.Call(rpcMethod, request, &reply)

	return err
}

func (w *Wrapper) forwardCommandToLeader(method string, args ...interface{}) error {
	arg := Command{
		Method: method,
		Args:   args,
	}

	var b []byte

	b, err := json.Marshal(arg)

	if err != nil {
		return err
	}

	return w.forwardToLeader("ForwardingClient.Apply", b)
}

func (w *Wrapper) forwardAddNodeToLeader(addr string) error {
	return w.forwardToLeader("ForwardingClient.AddNode", addr)
}

func (w *Wrapper) forwardRemoveNodeToLeader(addr string) error {
	return w.forwardToLeader("ForwardingClient.RemoveNode", addr)
}

//Mutate performs an operation that mutates your data.
func (w *Wrapper) Mutate(method string, args ...interface{}) error {

	if w.fsm.raft.State() != raft.Leader {
		return w.forwardCommandToLeader(method, args...)
	}

	var cmd = Command{
		Method: method,
		Args:   args,
	}

	b, err := json.Marshal(cmd)

	if err != nil {
		return err
	}

	f := w.fsm.raft.Apply(b, raftTimeout)

	if f.Error() != nil {
		return f.Error()
	}

	if f.Response() != nil && f.Response().(error) != nil {
		return f.Response().(error)
	}

	return nil

}

//AddNode adds a node, located at addr, to the cluster. The node must be ready to respond to Raft
//commands at the address.
func (w *Wrapper) AddNode(addr string) error {
	if w.fsm.raft.State() != raft.Leader {
		return w.forwardAddNodeToLeader(addr)
	}

	f := w.fsm.raft.AddPeer(addr)
	if f.Error() != nil {
		return f.Error()
	}
	return nil
}

//RemoveNode removes a node, located at addr, from the cluster.
func (w *Wrapper) RemoveNode(addr string) error {
	if w.fsm.raft.State() != raft.Leader {
		return w.forwardRemoveNodeToLeader(addr)
	}

	f := w.fsm.raft.RemovePeer(addr)
	if f.Error() != nil {
		return f.Error()
	}
	return nil
}

//SubscribeFunc executes the `Callback` func when the distributed object is mutated by applying `Mutate` on `method`.
//The callback should not mutate the interface or strange things will happen.
func (w *Wrapper) SubscribeFunc(method string, f Callback) {
	w.callbacks[method] = append(w.callbacks[method], f)
}

//SubscribeChan sends values to the returned channel when the underlying structure is mutated.
//The callback should not mutate the interface or strange things will happen.
func (w *Wrapper) SubscribeChan(method string, c chan *Mutation) {
	w.callbackChans[method] = append(w.callbackChans[method], c)
}

//Inspect gives f access to the distributed data. While f is executing, no goroutine may mutate
//the data. Multiple goroutines can call Inspect concurrently.
//f should not mutate the value or strange things will happen.
func (w *Wrapper) Inspect(f func(interface{})) {
	w.RLock()
	defer w.RUnlock()
	f(w.value)
}

func readPeersJSON(path string) ([]string, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}

	if len(b) == 0 {
		return nil, nil
	}

	var peers []string
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&peers); err != nil {
		return nil, err
	}

	return peers, nil
}

//parses host:port string and increments port by incr.
func incrementPort(addr string, incr int) (string, error) {
	var (
		portStr string
		host    string
		port    int
		err     error
	)
	host, portStr, err = net.SplitHostPort(addr)

	if err != nil {
		return "", err
	}

	port, err = strconv.Atoi(portStr)

	if err != nil {
		return "", err
	}

	port += incr

	return host + ":" + strconv.Itoa(port), nil
}
