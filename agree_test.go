package agree

import (
	"testing"
)

type testStruct struct {
	Value string
}

func (t *testStruct) Set(newValue string) {
	t.Value = newValue
}

func TestSingleNode(t *testing.T){
	s := &testStruct{}
	wt, err := Wrap(s, &Config{})
	var changed bool 
	
	if err != nil {
		t.Fatalf("Failed to wrap: %s", err.Error())
	}
	
	notify := func(newStruct interface{}, args ...interface{}){
		if len(args) != 1 {
			t.Fatalf("Wrong number of arguments: expected %d but got %d", 1, len(args))
		}
		argVal, ok := args[0].(string)
		
		if !ok {
			t.Fatal("Arg had incorrect type")
		}
		
		if argVal != "hello" {
			t.Fatalf("Expected arg to be %s but got %s", "hello", argVal)
		}
		
		changed = true		
	}
	
	wt.Subscribe("Set", notify)
	
	err = wt.Mutate("Set", "hello")
	
	if err != nil {
		t.Fatalf("Failed to mutate: %s", err.Error())
	}
	
	if !changed {
		t.Fatal("Callback did not get called")
	}
	
	wt.Inspect(func(val interface{}){
		v, ok := val.(testStruct)
		
		if !ok {
			t.Fatal("Value of incorrect type")
		}
		
		if v.Value != "hello" {
			t.Fatalf("Expected Value to be %s but got %s", "hello", v.Value)
		}
	})
	
	copiedVal, err := wt.Copy()
	
	if err != nil {
		t.Fatalf("Error copying: %s", err.Error())
	}
	
	copiedStruct, ok := copiedVal.(testStruct)
	
	if !ok {
		t.Fatal("Copied value of incorrect type")
	}
	
	if copiedStruct.Value != "hello" {
		t.Fatalf("Expected copied struct to have Value %s but had value %s", "hello", copiedStruct.Value)
	}
}