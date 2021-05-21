package server

type (
	Handle     func(ctrl *Controller)
	Controller struct {
	}
)

func (self *Controller) Handle(hd Handle) {
	hd(self)
}

func (self *Controller) Done() {

}
