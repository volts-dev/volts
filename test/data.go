package test

import (
	"context"
	"fmt"
)

type (
	Args struct {
		Num1 int
		Num2 int
		Str  string
		Flt  float64
	}

	Reply struct {
		Num int
		Str string
		Flt float64
	}

	Arith int
)

func (t *Arith) Mul(ctx context.Context, args *Args, reply *Reply) error {
	reply.Num = args.Num1 * args.Num2
	fmt.Println("Mul", reply.Num)
	return nil
}
