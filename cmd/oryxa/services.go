package main

import (
	"github.com/Shubham04-oss/oryxa-go/email"
	"github.com/Shubham04-oss/oryxa-go/files"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/teams"
)

// definitions are the services this binary carries, in the order they
// migrate, mount and — in reverse — close. Teams migrates before rag because
// its migrations install the vector extension rag's tables are declared with.
var definitions = []host.Definition{
	teams.Definition,
	files.Definition,
	email.Definition,
}
