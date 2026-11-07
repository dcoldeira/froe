package main

import "testing"

// Bare `froe` opens chat the way `claude` opens its session; every named
// command, and the flags that mean something of their own, keep theirs.
func TestChatByDefault(t *testing.T) {
	for _, tc := range []struct {
		args []string
		tty  bool
		want bool
	}{
		{nil, true, true},
		{nil, false, false}, // a script or a pipe gets the usage text
		{[]string{"-resume", "last"}, true, true},
		{[]string{"-no-context"}, true, true},
		{[]string{"chat"}, true, false}, // the switch runs it by name
		{[]string{"do", "fix it"}, true, false},
		{[]string{"rpc"}, false, false},
		{[]string{"--rpc"}, false, false},
		{[]string{"-h"}, true, false},
		{[]string{"--help"}, true, false},
		{[]string{"-v"}, true, false},
		{[]string{"--version"}, true, false},
	} {
		if got := chatByDefault(tc.args, tc.tty); got != tc.want {
			t.Errorf("chatByDefault(%q, tty=%v) = %v, want %v", tc.args, tc.tty, got, tc.want)
		}
	}
}
