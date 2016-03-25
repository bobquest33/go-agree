# Counter Example

This example is a distributed counter. You enter some text on stdin and the number of characters in that text will be added to the counter.

## Building

Running `build.sh` in this directory will create two folders: `ex1` and `ex2`. Each process needs its own directory as the Raft implementation needs exclusive access to its snapshot files.

## Running

Open a terminal and run `ex1/counter -p :8082`. Open a second terminal and run `ex2/counter -r :8082 -p :8080`.

Enter some text in either window and press enter. You will see the counter being incremented in both windows.