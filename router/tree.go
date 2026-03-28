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
'/web/content/{string:xmlid}',
'/web/content/{string:xmlid}/{string:filename}',
'/web/content/{int:id}',
'/web/content/{int:id}/{string:filename}',
'/web/content/{int:id}-{string:unique}',
'/web/content/{int:id}-{string:unique}/{string:filename}',
'/web/content/{string:model}/{int:id}/{string:field}',
'/web/content/{string:model}/{int:id}/{string:field}/{string:filename}'
*/
const (
	StaticNode  NodeType = iota // static, should equal
	VariantNode                 // named node, match a non-/ is ok
	AnyNode                     // catch-all node, match any
	RegexpNode                  // regex node, should match

	AllType ContentType = iota
	NumberType
	CharType

	LBracket = '{'
	RBracket = '}'
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
		Values      []string // For enum values like [read|write]
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
	self.RLock()
	defer self.RUnlock()

	services = make(map[*TGroup][]*registry.Endpoint)
	validator := make(map[string]*route)
	var walk func(node *treeNode)
	walk = func(node *treeNode) {
		if node.Route != nil && node.Route.group != nil && node.Route.group.config.IsService {
			grp := node.Route.group
			// TODO 检测
			if _, ok := validator[node.Route.Path]; !ok {
				//
			}

			services[grp] = append(services[grp], RouteToEndpiont(node.Route))
		}

		for _, c := range node.Children {
			walk(c)
		}
	}

	for _, node := range self.root {
		walk(node)
	}

	return services
}

// 添加路由到Tree
func (self *TTree) AddRoute(route *route) error {
	if route == nil {
		return nil
	}

	self.Lock()
	defer self.Unlock()

	if len(route.Methods) == 0 {
		return fmt.Errorf("route methods cannot be empty")
	}

	for _, method := range route.Methods {
		method = strings.ToUpper(method)
		delimitChar := route.PathDelimitChar

		// to parse path as a List node
		nodes, _ := self.parsePath(route.Path, delimitChar)
		// 验证合法性
		if !validNodes(nodes) {
			log.Panicf("express %s is not supported", route.Path)
		}

		// 绑定Route到最后一个Node
		node := nodes[len(nodes)-1]
		route.Action = node.Text // 赋值Action
		node.Route = route
		node.Path = route.Path // 存储路由绑定的URL

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

	self.Lock()
	defer self.Unlock()

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
			p = self.delNode(p, nodes, idx)
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
	buf := &bytes.Buffer{}
	buf.WriteString("Print routes tree:\n")
	for method, node := range self.root {
		if len(node.Children) > 0 {
			buf.WriteString(method)
			buf.WriteByte('\n')
			printNode(buf, 1, node, "")
			buf.WriteByte('\n')
		}
	}
	log.Info(buf.String())
}

func (r *TTree) Match(method string, path string) (*route, Params) {
	r.RLock()
	root := r.root[method]
	r.RUnlock()

	if root == nil {
		return nil, nil
	}

	var delimitChar byte = '/'
	if method == "CONNECT" {
		delimitChar = '.'
	} else {
		prefix_char := string(r.PrefixChar)
		// trim the Url to including "/" on begin of path
		if path != prefix_char && !strings.HasPrefix(path, prefix_char) {
			path = prefix_char + path
		}
	}

	params := make(Params, 0, strings.Count(path, string(delimitChar)))
	for _, n := range root.Children {
		e := r.matchNode(n, path, delimitChar, &params)
		if e != nil {
			return e.Route, params
		}
	}

	return nil, nil
}

func (r *TTree) matchNode(node *treeNode, path string, delimitChar byte, params *Params) *treeNode {
	switch node.Type {
	case StaticNode:
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
	case AnyNode:
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

	case VariantNode:
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
			if !validType(path, node.ContentType) {
				return nil
			}

			if len(node.Values) > 0 {
				matched := false
				for _, v := range node.Values {
					if path == v {
						matched = true
						break
					}
				}
				if !matched {
					return nil
				}
			}

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

					if len(node.Values) > 0 {
						matched := false
						for _, v := range node.Values {
							if path[:idx] == v {
								matched = true
								break
							}
						}
						if !matched {
							continue
						}
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

	case RegexpNode:

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
		p = self.addNode(p, nodes, idx, isHook)
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
		i, startOffset int
		level          int
		target         *treeNode
		node           *treeNode
	)
	nodes := make([]*treeNode, 0)
	isDyn := false
	l := len(path)

	for i = 0; i < l; i++ {
		switch path[i] {
		case delimitChar:
			if i > startOffset {
				nodes = append(nodes, &treeNode{Type: StaticNode, Text: path[startOffset:i]})
			}
			startOffset = i
			target = nil
			level = 0

		case LBracket:
			// Save static part before bracket
			if i > startOffset {
				nodes = append(nodes, &treeNode{Type: StaticNode, Text: path[startOffset:i]})
			}

			// Find closing bracket
			start := i + 1
			bracketLevel := 1
			for i++; i < l && bracketLevel > 0; i++ {
				if path[i] == LBracket {
					bracketLevel++
				} else if path[i] == RBracket {
					bracketLevel--
				}
			}
			if bracketLevel > 0 {
				panic(fmt.Sprintf("lack of %v", string(RBracket)))
			}

			content := path[start : i-1]
			// i now points to character after RBracket

			// Parse content: {name}, {name:type}, {name:type[pattern]}, {name:[pattern]}, {type:name}
			var name, typStr, pattern string
			if idx := strings.Index(content, ":"); idx != -1 {
				p1 := content[:idx]
				p2 := content[idx+1:]

				// Format: {name:type} or {name:type[pattern]} or {name:[pattern]}
				name = p1
				if bIdx := strings.Index(p2, "["); bIdx != -1 {
					typStr = p2[:bIdx]
					pattern = p2[bIdx:]
				} else {
					typStr = p2
				}
			} else {
				// Format: {name}
				name = content
			}

			// Further handle pattern if it's like {name:[pattern]}
			if name != "" && strings.ContainsAny(name, "()[]|*+?.") {
				// Old style format check: if name contains regex chars, it's actually a pattern
				pattern = name
				name = "var"
			}

			var typ ContentType = AllType
			switch typStr {
			case "int":
				typ = NumberType
			case "string":
				typ = CharType
			}

			if pattern != "" {
				if strings.HasPrefix(pattern, "[") && strings.HasSuffix(pattern, "]") {
					pattern = pattern[1 : len(pattern)-1]
				}

				// Optimize common regex patterns to VariantNode
				isRegex := true
				var enumValues []string

				if pattern == "0-9]+" || pattern == "\\d+" {
					typ = NumberType
					isRegex = false
				} else if pattern == "a-z]+" || pattern == "A-Za-z]+" || pattern == "a-zA-Z]+" {
					typ = CharType
					isRegex = false
				} else if !strings.ContainsAny(pattern, "()[]*+?.^$\\") {
					// Treat as enum pattern like "read|write"
					enumValues = strings.Split(pattern, "|")
					isRegex = false
				}

				if isRegex {
					node = &treeNode{Type: RegexpNode, regexp: regexp.MustCompile("^(" + pattern + ")$"), Text: ":" + name}
				} else {
					node = &treeNode{Type: VariantNode, Level: level + 1, ContentType: typ, Values: enumValues, Text: ":" + name}
				}
			} else {
				node = &treeNode{Type: VariantNode, Level: level + 1, ContentType: typ, Text: ":" + name}
			}

			nodes = append(nodes, node)
			isDyn = true

			// Level logic
			if level == 0 {
				target = node
			}
			level++

			if (i == l) || (i < l && path[i] == delimitChar) {
				level++
				target.Level = level
				target = nil
				level = 0
			}

			startOffset = i
			i-- // Adjust for loop i++

		case '*':
			if i > startOffset {
				nodes = append(nodes, &treeNode{Type: StaticNode, Text: path[startOffset:i]})
			}
			startOffset = i
			i++
			for ; i < l && utils.IsAlnumByte(path[i]); i++ {
			}
			nodes = append(nodes, &treeNode{Type: AnyNode, Level: -1, Text: path[startOffset:i]})
			isDyn = true
			startOffset = i
			i--
			if i == l-1 {
				return nodes, isDyn
			}

		default:
			// Normal character
		}
	}

	if target != nil {
		target.Level = level
	}

	if startOffset < l {
		nodes = append(nodes, &treeNode{Type: StaticNode, Text: path[startOffset:l]})
	}

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
		sort.Sort(target.Children)
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

	// Check enums
	if len(self.Values) != len(node.Values) {
		return false
	}
	for i := range self.Values {
		if self.Values[i] != node.Values[i] {
			return false
		}
	}

	// Check regex
	if (self.regexp == nil) != (node.regexp == nil) {
		return false
	}
	if self.regexp != nil && self.regexp.String() != node.regexp.String() {
		return false
	}

	return true
}

// add node nodes[i] to parent node p
func (self *TTree) addNode(parent *treeNode, nodes []*treeNode, i int, isHook bool) *treeNode {
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
					self.Count.Inc()
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

func (self *TTree) delNode(parent *treeNode, nodes []*treeNode, i int) *treeNode {
	// 如果:找到[已经注册]的分支节点则从该节继续[查找/添加]下一个节点
	for _, n := range parent.Children {
		if n.Equal(nodes[i]) {
			// 如果:插入的节点层级已经到末尾,则为该节点注册路由
			if i == len(nodes)-1 {
				// 剥离目标控制器
				n.Route.StripHandler(nodes[i].Route)
				self.Count.Dec() // 递减计数器
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
