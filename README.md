# Readme

Agree is a package that helps you create consistent distributed data structures using Raft. 

## Interface

Everything starts from a type that you want to wrap and distribute. 

```go

type KVStore map[string]string  // The type you want to replicate via Raft


/* Some methods we want to define on our type */

// Set sets the key `key` to value `value`
func (k KVStore) Set(key string, value string) {
	k[key] = value	// Note: no locking necessary
}

// Get returns the value stored at `key` - blank if none. The bool return parameter
// is used to indicate whether the key was present in the map.
func (k KVStore) Get(key string) (string, bool) {
	return k[key]  // Note: no locking necessary
}

```

Say you want to replicate a key-value store via Raft. You have the type, you've defined its methods, and they pass your unit tests.

Now you want a simple way to turn this into a distributed type that consistently replicates changes to all participating nodes in your cluster.

Go-agree wraps your type in a wrapper that takes care of that:

```go

w, err := agree.Wrap(make(KVStore), &agree.Config{})

```

Read the godoc for configuration options - they include things like addresses of other nodes in the cluster.


### Mutations

Now you have a wrapper, we can either mutate it or observe mutations. We're basically creating a simple key-value store here, so let's set a value:

```go

err := w.Mutate("Set", "key", "value")

```

Go-agree knows that your type had a "Set" method and invokes it for you, dealing with things like figuring out who the leader of the cluster is and storing snapshots of your type's value in case bad things happen.

**You should only mutate your value with the "Mutate" method**

At any time you can inspect your type, that is, "read" it (without mutating it). Go-agree takes care of locking so you don't need to:

```go
w.Inspect(func(val interface{}){
	fmt.Println(val.(KVStore))	
})
```

### Observing Mutations

You can subscribe to mutations, receiving notifications by executing a callback function of your choice or through a channel.

```go
	//Subscribe to mutations via callback function. No locking necessary.
	w.SubscribeFunc("Set", function(m agree.Mutation){
		fmt.Println("[func] KV store changed: ", m.NewValue)
	})
```

Have a look at the Godoc for the structure of the `Mutation` type - it also tells you how the mutation occurred. 

If you subscribe via a channel, Go-agree doesn't know when you'll access the value so you need to take care of locking yourself:

```go
	//Subscribe to mutations via channel.
	c := make(chan agree.Mutation, 3)
	w.SubscribeChan("Set", c)
	
	for {
		mutation := <-c
		c.RLock() // If subscribing via channel, make sure to RLock()/RUnlock() the wrapper.
		fmt.Println("KV store changed: ", mutation)
		c.RUnlock()
	}

```

### Cluster Membership Changes

If nodes join or leave the cluster just call the `Join()` and `Leave()` methods.

## Full Example (Key-value store)

Here is the full working example for a single-node cluster. Hopefully it is obvious how to extend it to multi-node with the appropriate configuration but if not please take a look in the "examples" directory.

```go

package main

import 

(
	agree "github.com/michaelbironneau/go-agree"
	"fmt"
	"time"
)

type KVStore map[string]string

// Set sets the key `key` to value `value`
func (k KVStore) Set(key string, value string) {
	k[key] = value	// Note: no locking necessary
}

// Get returns the value stored at `key` - blank if none. The bool return parameter
// is used to indicate whether the key was present in the map.
func (k KVStore) Get(key string) (string, bool) {
	return k[key]  // Note: no locking necessary
}

func main(){
	
	//Start single-node Raft
	w, err := agree.Wrap(make(KVStore), &agree.Config{})
	
	time.sleep(time.Second*5) // Give Raft ample time to set itself up. Depending on your use case this may not be necessary.
	
	if err != nil {
		fmt.Println("Error wrapping: ", err)
		return
	}
	
	//Set some keys
	w.Mutate("Set", "key", "value")
	w.Mutate("Set", "key2", "value2")
	
	//Get some values back. No locking necessary.
	w.Inspect(func(val interface{}){
		k := val.(KVStore)
		fmt.Println(k.Get("key2"))	
	})
	
	//Subscribe to mutations via channel.
	c := make(chan agree.Mutation, 3)
	w.SubscribeChan("Set", c)
	
	//Subscribe to mutations via callback function. No locking necessary.
	w.SubscribeFunc("Set", function(m agree.Mutation){
		fmt.Println("[func] KV store changed: ", m)
	})
	
	w.Mutate("Set", "key3", "value3")
	go w.Mutate("Set", "Key", "value4")
	
	for {
		mutation := <-c
		c.RLock() // If subscribing via channel, make sure to RLock()/RUnlock() the wrapper.
		fmt.Println("KV store changed: ", mutation)
		c.RUnlock()
	}
			
}


```



