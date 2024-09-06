package router

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/registry"
	"go.uber.org/atomic"
)

/*
tree 负责路由树的解析,排序,匹配
实现前面加类型
'/web/content/<string:xmlid>',
'/web/content/<string:xmlid>/<string:filename>',
'/web/content/<int:id>',
'/web/content/<int:id>/<string:filename>',
'/web/content/<int:id>-<string:unique>',
'/web/content/<int:id>-<string:unique>/<string:filename>',
'/web/content/<string:model>/<int:id>/<string:field>',
'/web/content/<string:model>/<int:id>/<string:field>/<string:filename>'
*/
const (
	StaticNode  NodeType = iota // static, should equal
	VariantNode                 // named node, match a non-/ is ok
	AnyNode                     // catch-all node, match any
	RegexpNode                  // regex node, should match

	AllType ContentType = iota
	NumberType
	CharType

	LBracket = '<'
	RBracket = '>'
)

var (
	nodeType = map[NodeType]string{
		StaticNode:  "Static", // static, should equal
		VariantNode: "Var",    // named node, match a non-/ is ok
		AnyNode:     "Any",    // catch-all node, match any
		RegexpNode:  "Reg",    // regex node, should match
	}

	contentType = map[ContentType]string{
		AllType:    "all",
		NumberType: "int",
		CharType:   "string",
	}
)

type NodeType byte // 节点类型
func (self NodeType) String() string {
	return [...]string{"StaticNode", "VariantNode", "AnyNode", "RegexpNode"}[self]
}

type ContentType byte // 变量类型
func (self ContentType) String() string {
	return [...]string{"AllType", "NumberType", "CharType"}[self]
}

type (
	param struct {
		Name  string
		Value string
	}

	Params []param

	// 配置函数接口
	ConfigOption func(*TTree)

	// 使用Sort 接口自动排序
	subNodes []*treeNode

	treeNode struct {
		Route       *route
		Type        NodeType
		ContentType ContentType
		Children    subNodes
		Text        string // Path string /web/
		Path        string
		Level       int // #动态Node排序等级 /.../ 之间的Nodes越多等级越高
		regexp      *regexp.Regexp
	}

	// safely tree
	TTree struct {
		sync.RWMutex  // lock for conbine action
		root          map[string]*treeNode
		Text          string
		IgnoreCase    bool
		__DelimitChar byte // Delimit Char xxx<.>xxx
		PrefixChar    byte // the Prefix Char </>xxx.xxx
		Count         atomic.Int32
	}
)

func (p *Params) Get(key string) string {
	for _, v := range *p {
		if v.Name == key {
			return v.Value
		}
	}
	return ""
}

func (p *Params) Set(key, value string) {
	for i, v := range *p {
		if v.Name == key {
			(*p)[i].Value = value
			return
		}
	}
}

func (p *Params) SetParams(params []param) {
	*p = params
}

func (self subNodes) Len() int {
	return len(self)
}

func (self subNodes) Swap(i, j int) {
	self[i], self[j] = self[j], self[i]
}

// static route will be put the first, so it will be match first.
// two static route, content longer is first.
func (self subNodes) Less(i, j int) bool {
	if self[i].Type == StaticNode {
		if self[j].Type == StaticNode {
			return len(self[i].Text) > len(self[j].Text)
		}
		return true
	}

	if self[j].Type == StaticNode {
		return false
	} else {
		return self[i].Level > self[j].Level
	}

	//return i < j
}

func NewRouteTree(opts ...ConfigOption) *TTree {
	tree := &TTree{
		root:          make(map[string]*treeNode),
		__DelimitChar: 0, // !NOTE! 默认为未定义 以便区分RPC
		PrefixChar:    '/',
	}
	tree.Init(opts...)
	return tree
}

func (self *TTree) Init(opts ...ConfigOption) {
	for _, cfg := range opts {
		cfg(self)
	}
}

func (self *TTree) Endpoints() (services map[*TGroup][]*registry.Endpoint) {
	services = make(map[*TGroup][]*registry.Endpoint, 0)
	validator := make(map[string]*route)
	var match func(method string, i int, node *treeNode)
	match = func(method string, i int, node *treeNode) {
		for _, c := range node.Children {
			if c.Route != nil && c.Route.group != nil {
				grp := c.Route.group
				// TODO 检测
				if _, ok := validator[c.Route.Path]; !ok {
					//
				}

				if eps, has := services[grp]; has {
					services[grp] = append(eps, RouteToEndpiont(c.Route))
				} else {
					services[grp] = []*registry.Endpoint{RouteToEndpiont(c.Route)}
				}
			}
			match(method, i+1, c)
		}
	}

	for method, node := range self.root {
		if len(node.Children) > 0 {
			match(method, 1, node)
		}
	}

	return services
}

// 添加路由到Tree
func (self *TTree) AddRoute(route *route) error {
	if route == nil {
		return nil
	}

	for _, method := range route.Methods {
		method = strings.ToUpper(method)

		delimitChar := route.PathDelimitChar

		// to parse path as a List node
		nodes, _ := self.parsePath(route.Path, delimitChar)

		// 绑定Route到最后一个Node
		node := nodes[len(nodes)-1]
		route.Action = node.Text // 赋值Action
		node.Route = route
		node.Path = route.Path // 存储路由绑定的URL

		// 验证合法性
		if !validNodes(nodes) {
			log.Panicf("express %s is not supported", route.Path)
		}

		// insert the node to tree
		self.addNodes(method, nodes, false)
	}

	return nil
}

// delete the route
func (self *TTree) DelRoute(path string, route *route) error {
	if route == nil {
		return nil
	}

	for _, method := range route.Methods {
		n := self.root[method]
		if n == nil {
			return nil
		}

		var delimitChar byte = '/'
		if method == "CONNECT" {
			delimitChar = '.'
		}

		// to parse path as a List node
		nodes, _ := self.parsePath(path, delimitChar)

		var p *treeNode = n // 复制方法对应的Root

		// 层级插入Nodes的Node到Root
		for idx := range nodes {
			p = n.delNode(self, p, nodes, idx)
		}
	}

	return nil
}

// conbine 2 tree together
func (self *TTree) Conbine(from *TTree) *TTree {
	// NOTE 避免合并不同分隔符的路由树 不应该发生
	if len(self.root) > 0 && len(from.root) > 0 { // 非空的Tree
		if self.__DelimitChar != from.__DelimitChar { // 分隔符对比
			log.Panicf("could not conbine 2 different kinds (RPC/HTTP) of routes tree!")
			return self
		}
	}

	self.Lock()
	defer self.Unlock()
	for method, new_node := range from.root {
		if main_nodes, has := self.root[method]; !has {
			self.root[method] = new_node
		} else {
			for _, node := range new_node.Children {
				self.conbine(main_nodes, node)
			}
		}
	}

	return self
}

func (self *TTree) PrintTrees() {
	buf := bytes.NewBufferString("")
	buf.WriteString("Print routes tree:\n")
	for method, node := range self.root {
		if len(node.Children) > 0 {
			buf.WriteString(method + "\n")
			printNode(buf, 1, node, "")
			buf.WriteString("\n")
		}
	}
	log.Info(buf.String())
}

func (r *TTree) Match(method string, path string) (*route, Params) {
	var delimitChar byte = '/'
	if method == "CONNECT" {
		delimitChar = '.'
	}

	if root := r.root[method]; root != nil {
		prefix_char := string(r.PrefixChar)
		// trim the Url to including "/" on begin of path
		if !strings.HasPrefix(path, prefix_char) && path != prefix_char {
			path = prefix_char + path
		}

		var params = make(Params, 0, strings.Count(path, string(delimitChar)))
		for _, n := range root.Children {
			e := r.matchNode(n, path, delimitChar, &params)
			if e != nil {
				return e.Route, params
			}
		}
	}

	return nil, nil
}

func (r *TTree) matchNode(node *treeNode, path string, delimitChar byte, params *Params) *treeNode {
	if node.Type == StaticNode { // 静态节点
		// match static node
		if strings.HasPrefix(path, node.Text) {
			if len(path) == len(node.Text) {
				return node
			}

			for _, c := range node.Children {
				e := r.matchNode(c, path[len(node.Text):], delimitChar, params)
				if e != nil {
					return e
				}
			}
		}

	} else if node.Type == AnyNode { // 全匹配节点
		for _, c := range node.Children {
			idx := strings.LastIndex(path, c.Text)
			if idx > -1 {
				h := r.matchNode(c, path[idx:], delimitChar, params)
				if h != nil {
					*params = append(*params, param{node.Text[1:], path[:idx]})
					return h
				}

			}
		}

		*params = append(*params, param{node.Text[1:], path})
		return node

	} else if node.Type == VariantNode { // 变量节点
		// # 消除path like /abc 的'/'
		// 根据首字符判断接下来的处理条件
		first_Char := path[0]
		if first_Char == delimitChar {
			for _, c := range node.Children {
				h := r.matchNode(c, path[0:], delimitChar, params)
				if h != nil {
					*params = append(*params, param{node.Text[1:], path[:0]})
					return h
				}
			}
			return nil
		}

		isLast := strings.IndexByte(path, delimitChar) == -1
		if (isLast || len(node.Children) == 0) && node.Route != nil { // !NOTE! 匹配到最后一个条件
			*params = append(*params, param{node.Text[1:], path})
			return node
		} else { // !NOTE! 匹配回溯 当匹配进入错误子节点返回nil到父节点重新匹配父节点
			var retnil bool
			for _, c := range node.Children {
				idx := strings.Index(path, c.Text) // #匹配前面检索到的/之前的字符串
				if idx > -1 {
					if len(path[:idx]) > 1 && strings.IndexByte(path[:idx], delimitChar) > -1 {
						retnil = true
						continue
					}

					if !validType(path[:idx], node.ContentType) {
						continue
					}

					h := r.matchNode(c, path[idx:], delimitChar, params)
					if h != nil {
						*params = append(*params, param{node.Text[1:], path[:idx]})
						return h
					}
					retnil = true
				}
			}

			if retnil || len(node.Children) > 0 {
				return nil
			}
		}
	} else if node.Type == RegexpNode { // 正则节点
		idx := strings.IndexByte(path, delimitChar)
		if idx > -1 {
			if node.regexp.MatchString(path[:idx]) {
				for _, c := range node.Children {
					h := r.matchNode(c, path[idx:], delimitChar, params)
					if h != nil {
						*params = append(*params, param{node.Text[1:], path[:idx]})
						return h
					}
				}
			}
		} else {
			for _, c := range node.Children {
				idx := strings.Index(path, c.Text)
				if idx > -1 && node.regexp.MatchString(path[:idx]) {
					h := r.matchNode(c, path[idx:], delimitChar, params)
					if h != nil {
						*params = append(*params, param{node.Text[1:], path[:idx]})
						return h
					}
				}
			}

			if node.regexp.MatchString(path) {
				*params = append(*params, param{node.Text[1:], path})
				return node
			}
		}
	}

	return nil
}

// add nodes to trees
func (self *TTree) addNodes(method string, nodes []*treeNode, isHook bool) {
	// 获得对应方法[POST,GET...]
	cn := self.root[method]
	if cn == nil {
		// 初始化Root node
		cn = &treeNode{
			Children: subNodes{},
		}
		self.root[method] = cn
	}

	var p *treeNode = cn // 复制方法对应的Root

	// 层级插入Nodes的Node到Root
	for idx := range nodes {
		p = cn.addNode(self, p, nodes, idx, isHook)
	}
}

// 解析Path为Node
/*   /:name1/:name2 /:name1-:name2 /(:name1)sss(:name2)
     /(*name) /(:name[0-9]+) /(type:name[a-z]+)
	Result: @ Nodes List
	        @ is it dyn route
*/
func (r *TTree) parsePath(path string, delimitChar byte) ([]*treeNode, bool) {
	if path == "" {
		panic("path cannot be empty")
	}

	if delimitChar == '/' && path[0] != '/' {
		path = "/" + path
	}

	var (
		i, startOffset int // i 游标 J 上次标记
		bracket        int
		level          int       // #Node的 排序等级
		target         *treeNode // 记录等级的Node 一般为/ 开始的第一个动态
		node           *treeNode
	)
	// 默认
	nodes := make([]*treeNode, 0)
	isDyn := false
	l := len(path)
	//j = i - 1 // 当i==0时J必须小于它
	for ; i < l; i++ {
		switch path[i] {
		case delimitChar:
			// 创建Text:'/' Node
			if bracket == 0 && i > startOffset {
				//if path[j] == '/' {
				//	nodes = append(nodes, &treeNode{Type: StaticNode, Text: string(path[j])})
				//}
				//j++
				nodes = append(nodes, &treeNode{Type: StaticNode, Level: 0, Text: path[startOffset:i]})
				startOffset = i
			}

			// # 重置计数
			target = nil
			level = 0 // #开始计数

		case LBracket:
			bracket = 1

		case ':':
			var typ ContentType = AllType
			if path[i-1] == LBracket { //#like (:var)
				// 添加变量前的静态字符节点
				nodes = append(nodes, &treeNode{Type: StaticNode, Text: path[startOffset : i-bracket]})
				bracket = 1
			} else {
				// #为变量区分数据类型
				str := path[startOffset : i-bracket] // #like /abc1(string|upper:var)
				idx := strings.Index(str, string(LBracket))
				if idx == -1 {
					log.Fatalf("expect a %s near path %s position %d~%d", path, string(LBracket), startOffset, i)
				}
				nodes = append(nodes, &treeNode{Type: StaticNode, Text: str[:idx]})
				str = str[idx+1:]
				switch str {
				case "int":
					typ = NumberType
				case "string":
					typ = CharType
				default:
					typ = AllType
				}
				bracket = 1
			}

			startOffset = i
			var (
				regex string
				start = -1
			)

			if bracket == 1 {
				// 开始记录Pos
				for ; i < l && RBracket != path[i]; i++ { // 移动Pos到")" 遇到正则字符标记起
					if start == -1 && utils.IsSpecialByte(path[i]) { // 如果是正则
						start = i
					}
				}
				if path[i] != RBracket {
					panic(fmt.Sprintf("lack of %v", RBracket))
				}

				if start > -1 {
					regex = path[start:i] //正则内容
				}
			} else {
				i = i + 1
				for ; i < l && utils.IsAlnumByte(path[i]); i++ {
				}
			}

			if len(regex) > 0 { // 正则
				node = &treeNode{Type: RegexpNode, regexp: regexp.MustCompile("(" + regex + ")"), Text: path[startOffset : i-len(regex)]}
				nodes = append(nodes, node)
			} else { // 变量
				node = &treeNode{Type: VariantNode, Level: level + 1, ContentType: typ, Text: path[startOffset:i]}
				nodes = append(nodes, node)
			}

			isDyn = true    // #标记 Route 为动态
			i = i + bracket // #剔除")"字符 bracket=len(“)”)
			startOffset = i

			// 当计数器遇到/或者Url末尾时将记录保存于Node中
			if target != nil && ((i == l) || (i != l && path[startOffset+1] == delimitChar)) {
				level++
				target.Level = level

				// # 重置计数
				target = nil
				level = 0
			}

			if i == l {
				return nodes, isDyn
			}

			// #计数滴答
			// 放置在 i == l 后 确保表达式2比1多一个层级
			// @/(int:id1)-(:unique2)
			// @/(:id3)-(:unique3)/(:filename)
			if (i != l && path[startOffset] != delimitChar) || level != 0 {
				if level == 0 {
					target = node
				}

				level++
			}

		case '*':
			nodes = append(nodes, &treeNode{Type: StaticNode, Text: path[startOffset : i-bracket]})
			startOffset = i
			//if bracket == 1 {
			//	for ; i < l && RBracket == path[i]; i++ {
			//	}
			//} else {
			i = i + 1
			for ; i < l && utils.IsAlnumByte(path[i]); i++ {
			}
			//}
			nodes = append(nodes, &treeNode{Type: AnyNode, Level: -1, Text: path[startOffset:i]})
			isDyn = true    // 标记 Route 为动态
			i = i + bracket // bracket=len(“)”)
			startOffset = i
			if i == l {
				return nodes, isDyn
			}

		default:
			bracket = 0
		}
	}

	nodes = append(nodes, &treeNode{
		Type: StaticNode,
		Text: path[startOffset:i],
	})

	return nodes, isDyn
}

// conbine 2 node together
func (self *TTree) conbine(target, from *treeNode) {
	var exist_node *treeNode

	// 是否目标Node有该Node
	for _, n := range target.Children {
		if n.Equal(from) {
			exist_node = n
		}
	}
	// 如果:无该Node直接添加完成所有工作
	// 或者:遍历添加所有没有的新Node
	if exist_node == nil {
		target.Children = append(target.Children, from)
		return
	} else {
		if exist_node.Type == RegexpNode {

		}

		if from.Route != nil {
			if exist_node.Route == nil {
				exist_node.Route = from.Route
			} else {
				// 叠加合并Controller
				exist_node.Route.CombineHandler(from.Route)
			}
		}

		// conbine sub-node
		for _, n := range from.Children {
			self.conbine(exist_node, n)
		}
	}
}

func (self *treeNode) Equal(node *treeNode) bool {
	if self.Type != node.Type || self.Text != node.Text || self.ContentType != node.ContentType {
		return false
	}
	return true
}

// add node nodes[i] to parent node p
func (self *treeNode) addNode(tree *TTree, parent *treeNode, nodes []*treeNode, i int, isHook bool) *treeNode {
	if len(parent.Children) == 0 {
		parent.Children = make([]*treeNode, 0)
	}

	// 如果:找到[已经注册]的分支节点则从该节继续[查找/添加]下一个节点
	for _, n := range parent.Children {
		if n.Equal(nodes[i]) {
			// 如果:插入的节点层级已经到末尾,则为该节点注册路由
			if i == len(nodes)-1 {
				// 原始路由会被替换
				if isHook {
					n.Route.CombineHandler(nodes[i].Route)
				} else {
					n.Route = nodes[i].Route
					tree.Count.Inc()
				}
			}
			return n
		}
	}

	// 如果:该节点没有对应分支则插入同级的nodes为新的分支
	parent.Children = append(parent.Children, nodes[i])
	sort.Sort(parent.Children)
	return nodes[i]
}

func (self *treeNode) delNode(tree *TTree, parent *treeNode, nodes []*treeNode, i int) *treeNode {
	// 如果:找到[已经注册]的分支节点则从该节继续[查找/添加]下一个节点
	for _, n := range parent.Children {
		if n.Equal(nodes[i]) {
			// 如果:插入的节点层级已经到末尾,则为该节点注册路由
			if i == len(nodes)-1 {
				// 剥离目标控制器
				n.Route.StripHandler(nodes[i].Route)
				tree.Count.Dec() // 递减计数器
			}
			return n
		}
	}

	sort.Sort(parent.Children)
	return nodes[i]
}

func validType(content string, typ ContentType) bool {
	switch typ {
	case NumberType:
		for i := 0; i < len(content); i++ {
			if !utils.IsDigitByte(content[i]) {
				return false
			}
		}
	case CharType:
		for i := 0; i < len(content); i++ {
			if !utils.IsAlphaByte(content[i]) {
				return false
			}
		}
	default:
		// 所有字符串
		return true
	}

	return true
}

// validate parsed nodes, all non-static route should have static route children.
func validNodes(nodes []*treeNode) bool {
	if len(nodes) == 0 {
		return false
	}
	var lastTp = nodes[0]
	for _, node := range nodes[1:] {
		if lastTp.Type != StaticNode && node.Type != StaticNode {
			return false
		}
		lastTp = node
	}
	return true
}

func printNode(buf *bytes.Buffer, lv int, node *treeNode, path string) {
	cnt := len(node.Children)
	isLast := false
	subPath := ""
	for idx, c := range node.Children {
		isLast = idx == cnt-1

		// 计算子路径打印方式
		if isLast { // 如果是最后一个
			subPath = path + "     " // 空格距离
		} else {
			subPath = path + " |   "
		}

		buf.WriteString(path + " |-- ")
		buf.WriteString(fmt.Sprintf(`%s ==> Lv:%d  Type:%v  VarType:%v `, c.Text, c.Level, nodeType[c.Type], contentType[c.ContentType]))
		if c.Route != nil {
			if c.Route.group != nil {
				buf.WriteString(fmt.Sprintf(" (*%d Mod:%s)", len(c.Route.handlers), c.Route.group.String()))
			} else {
				buf.WriteString(fmt.Sprintf(" (*%d)", len(c.Route.handlers)))
			}
		}

		//if !reflect.DeepEqual(c.Route, route{}) {
		if c.Route != nil {
			//fmt.Print("  ", c.Route.HandleType.String())
			//fmt.Printf("  %p", c.handle.method.Interface())
		}
		buf.WriteString("\n")
		printNode(buf, lv+1, c, subPath)
	}
}

// 忽略大小写
func WithIgnoreCase() ConfigOption {
	return func(tree *TTree) {
		tree.IgnoreCase = true
	}
}
