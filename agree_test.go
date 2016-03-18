package agree

import (
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func init() {
	fmt.Println("Cleaning up previous Raft state...")
	exec.Command("rm", "-rf", "snapshots/*")
	exec.Command("rm", "raft.db")
}

type testStruct struct {
	Value string
}

func (t *testStruct) Set(newValue string) {
	t.Value = newValue
}

func TestSingleNode(t *testing.T) {
	s := &testStruct{}
	wt, err := Wrap(s, &Config{})

	var changed bool

	if err != nil {
		t.Fatalf("Failed to wrap: %s", err.Error())
	}

	notify := func(mutation Mutation) {
		if len(mutation.MethodArgs) != 1 {
			t.Fatalf("Wrong number of arguments: expected %d but got %d", 1, len(mutation.MethodArgs))
		}
		argVal, ok := mutation.MethodArgs[0].(string)

		if !ok {
			t.Fatal("Arg had incorrect type")
		}

		if argVal != "hello" {
			t.Fatalf("Expected arg to be %s but got %s", "hello", argVal)
		}

		changed = true
	}

	wt.SubscribeFunc("Set", notify)

	err = wt.Mutate("Set", "hello")

	time.Sleep(time.Second * 3)

	if err != nil {
		t.Fatalf("Failed to mutate: %s", err.Error())
	}

	if !changed {
		t.Fatal("Callback did not get called")
	}

	wt.Inspect(func(val interface{}) {
		v, ok := val.(*testStruct)

		if !ok {
			t.Fatal("Value of incorrect type", val)
		}

		if v.Value != "hello" {
			t.Fatalf("Expected Value to be %s but got %s", "hello", v.Value)
		}
	})
}
