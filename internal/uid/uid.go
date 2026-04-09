package uid

import (
	"github.com/bwmarrin/snowflake"
)

var uuid *snowflake.Node

func init() {
	var err error
	uuid, err = snowflake.NewNode(1)
	if err != nil {
		panic(err)
	}
}

func Next() int64 {
	return uuid.Generate().Int64()
}
