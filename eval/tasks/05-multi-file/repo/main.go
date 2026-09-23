package main

import (
	"fmt"

	"evaltask/internal/store"
)

func main() {
	fmt.Println(store.Fetch("a1"))
	fmt.Println(store.Fetch("b2"))
}
