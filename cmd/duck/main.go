// Command duck is the laptop-side client: the cobra command surface plus (from
// M3) the bubbletea session picker. It drives the hub over SSH and never links
// SQLite. Mirrors flok/main.go.
package main

import "github.com/DigiBugCat/duck/command"

func main() {
	command.Execute()
}
