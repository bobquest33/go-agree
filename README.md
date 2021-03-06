# Go Agree

[![Go Report Card](http://goreportcard.com/badge/github.com/michaelbironneau/go-agree)](https://goreportcard.com/report/github.com/michaelbironneau/go-agree)
[![Build Status](https://travis-ci.org/michaelbironneau/go-agree.svg?branch=master)](https://travis-ci.org/michaelbironneau/go-agree/)
[![MIT licensed](https://img.shields.io/badge/license-MIT-blue.svg)](https://raw.githubusercontent.com/michaelbironneau/go-agree/master/LICENSE.md)


[Godoc](https://godoc.org/github.com/michaelbironneau/go-agree)

Go-Agree is a proof-of-concept package that helps you create consistent replicated data structures using Raft. 

For any Gob-marshallable interface you provide (with Gob-marshallable method args), Go-Agree will give you a wrapper to

* apply commands that mutate the value of the interface (invoking the relevant methods on your interface through reflection)
* create/restore snapshots (stored in BoltDB)
* forward commands to the Raft leader (via net/rpc)
* inspect the interface at any time or subscribe to mutations of its value 

In the future it may also help you deal with partitioned data structures, probably using [Blance](https://github.com/couchbase/blance).

*This is at a proof-of-concept stage. Breaking changes to the API may occur. Suggestions, bug reports and PRs welcome.*

# Tutorial

## Step 1: Code up the data structure that you want to distribute

Here we're going to create a very simple key-value store.

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

## Step 2: Wrap your type

Now you want a simple way to turn this into a distributed type that consistently replicates changes to all participating nodes in your cluster.

Go-agree wraps your type in a wrapper that takes care of that:

```go

w, err := agree.Wrap(make(KVStore), &agree.Config{})

```

Read the godoc for configuration options - they include things like addresses of other nodes in the cluster and Raft configuration.

## Step 3: Making mutations and observing the result 

### Mutating the value

Now you have a wrapper, we can either mutate it or observe mutations. We're basically creating a simple key-value store here, so let's set a value:

```go

err := w.Mutate("Set", "key", "value")

```

Go-agree knows that your type had a "Set" method and invokes it for you, dealing with things like figuring out who the leader of the cluster is and storing snapshots of your type's value in case bad things happen.

**You should only mutate your value with the "Mutate" method, not by calling your interface's methods directly**

### Reading the value

At any time you can read the value, that is, invoke non-mutating methods on your interface. Go-agree takes care of locking and forwarding the read to the Raft leader so you don't need to.

There are three supported consistency levels for this:

* `Any`: Will invoke the method on the local node's copy of the value, which may be stale
* `Leader`: Will invoke the method on the local node if it thinks it is a leader, but will not check this (so there is a small window of inconsistency)
* `Consistent`: Linearizable. Will check to ensure that the node that thinks it is a leader is still a leader and will return an error if not (in that case you should retry).

```go
val, err := w.Read("Get", agree.Consistent, "key")
if err == nil {
	fmt.Println(len(val.(string)))
} else {
	fmt.Println("Error: ", err)	
}
```

*Your method must either return one value or two values (of which the second must be of Error type).*

If you retained a pointer to your interface before you wrapped it, you can also do something like this (just make sure to lock the wrapper when doing this):

```go
kv := make(KVStore)

w, _ := agree.Wrap(KVStore, &agree.Config{})

w.Mutate("Set", "key", "value")

w.RLock()
fmt.Println(kv["key"]) //prints "value"
w.RUnlock()

```

Please note that if you do this then you may be reading stale data.

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

To add a node to the cluster after you have Wrap()'ed your interface, use `AddNode()`. The node should be ready to receive Raft commands at that address.

```go
	err := w.AddNode("localhost:2345")
```

To remove a node from the cluster after you have Wrap()'ed your interface, use `RemoveNode()`:

```go
	err := w.RemoveNode("localhost:2345")
```



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
	
	//Start single-node Raft. Will block until a leader is elected.
	w, err := agree.Wrap(make(KVStore), &agree.Config{})

	if err != nil {
		fmt.Println("Error wrapping: ", err)
		return
	}
	
	//Set some keys
	w.Mutate("Set", "key", "value")
	w.Mutate("Set", "key2", "value2")
	
	//Get some values back with 'Any' consistency level.
	v,_ := w.Read("Get", agree.Any, "key2")
	fmt.Println(v)
	
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
		w.RLock() // If subscribing via channel, make sure to RLock()/RUnlock() the wrapper if you access NewValue field.
		fmt.Println("New value: ", mutation.NewValue)
		w.RUnlock()
		fmt.Println("It was mutated by calling ", mutation.Method)
		fmt.Println("...which was invoked with args ", mutation.MethodArgs)
	}
			
}


```

## Limitations and Caveats

There are a few things you should know:

* The interface{} you Wrap() should be Gob-marshallable *and* JSON-marshallable. All of its methods should have Gob-marshallable and JSON-marshallable arguments.
* You can Wrap() multiple interface{}s. However, cannot currently Wrap() multiple interfaces *of the same type*. If you attempt this the second Wrap() call will probably block. You can work around it by aliasing the two types `type T2 T1`.
* Go-Agree listens on two ports *per wrapped interface*: the Raft port (default 8080 - you can override it) and the command forwarding port (=Raft port + 1). 


## How it works

I'm using Hashicorp's Raft API as described in the excellent tutorial [here](https://github.com/otoolep/hraftd).

Reflection is used to map commit log entries to method invocations and net/rpc to forward commands to the leader.