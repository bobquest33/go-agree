package main

import (
	"bufio"
	"flag"
	"fmt"
	agree "github.com/michaelbironneau/go-agree"
	"os"
)

//Peers is a slice of nodes
type Peers []string

//Set adds a peer to the list of peers
func (p *Peers) Set(value string) error {
	*p = append(*p, value)
	return nil
}

func (p *Peers) String() string {
	return fmt.Sprintf("%v", *p)
}

//Counter is a float64 that can be incremented by an (possibly negative) int.
type Counter float64

//Increment increments the counter.
func (c *Counter) Increment(by float64) {
	fmt.Println("Incrementing...")
	*c += Counter(by)
}

func main() {
	var (
		c        Counter
		config   agree.Config
		peers    Peers
		raftBind string
	)

	//Parse command-line args
	flag.Var(&peers, "p", "List of peers")
	flag.StringVar(&raftBind, "r", ":8080", "Raft bind address eg ':8080'")
	flag.Parse()
	//Initialize wrapper
	if len(peers) > 0 {
		config.Peers = peers
	}
	config.RaftBind = raftBind
	fmt.Println(raftBind)
	w, err := agree.Wrap(&c, &config)

	if err != nil {
		fmt.Println(err)
		return
	}

	//Subscribe to mutations via the 'Increment' method
	w.SubscribeFunc("Increment", func(m agree.Mutation) {
		fmt.Println("New Value: ", float64(*m.NewValue.(*Counter)))
	})

	//Read input and for each input string increment the distributed counter
	//by its length.
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Print("Enter a string: ")
		text, _ := reader.ReadString('\n')
		err := w.Mutate("Increment", len(text)-1)
		if err != nil {
			fmt.Println("Error incrementing counter: ", err)
		}
	}
}
