package evaltask

import "testing"

func TestIsPalindrome(t *testing.T) {
	cases := map[string]bool{"racecar": true, "abba": true, "abc": false, "": true, "a": true}
	for in, want := range cases {
		if got := IsPalindrome(in); got != want {
			t.Errorf("IsPalindrome(%q) = %v, want %v", in, got, want)
		}
	}
}
