package server

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/volts-dev/logger"
	"github.com/volts-dev/utils"
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
)

var (
	HttpMethods = []string{
		"GET",
		"POST",
		"HEAD",
		"DELETE",
		"PUT",
		"OPTIONS",
		"TRACE",
		"PATCH",
	}

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

type (
	NodeType    byte // 节点类型
	ContentType byte // 变量类型

	param struct {
		Name  string
		Value string
	}

	Params []param

	// 配置函数接口
	ConfigOption func(*TTree)

	// 使用Sort 接口自动排序
	TSubNodes []*TNode

	TNode struct {
		Route       *TRoute
		Type        NodeType
		ContentType ContentType
		Children    TSubNodes
		Text        string // Path string /web/
		Path        string
		Level       int // #动态Node排序等级 /.../ 之间的Nodes越多等级越高
		regexp      *regexp.Regexp
	}

	// safely tree
	TTree struct {
		Text        string
		IgnoreCase  bool
		DelimitChar byte // Delimit Char xxx<.>xxx
		PrefixChar  byte // the Prefix Char </>xxx.xxx

		root         map[string]*TNode
		sync.RWMutex // lock for conbine action
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

func (self TSubNodes) Len() int {
	return len(self)
}

func (self TSubNodes) Swap(i, j int) {
	self[i], self[j] = self[j], self[i]
}

// static route will be put the first, so it will be match first.
// two static route, content longer is first.
func (self TSubNodes) Less(i, j int) bool {
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

func NewRouteTree(config_fn ...ConfigOption) *TTree {
	tree := &TTree{
		root:        make(map[string]*TNode),
		DelimitChar: 0, // !NOTE! 默认为未定义 以便区分RPC
		PrefixChar:  '/',
	}

	for _, cfg := range config_fn {
		cfg(tree)
	}
	return tree
}

// 解析Path为Node
/*   /:name1/:name2 /:name1-:name2 /(:name1)sss(:name2)
     /(*name) /(:name[0-9]+) /(type:name[a-z]+)
	Result: @ Nodes List
	        @ is it dyn route
*/
func (r *TTree) parsePath(path string, delimitChar byte) (nodes []*TNode, isDyn bool) {
	if path == "" {
		panic("path cannot be empty")
	}

	if delimitChar == '/' && path[0] != '/' {
		path = "/" + path
	}

	var (
		i, startOffset int // i 游标 J 上次标记

		bracket int
		level   int    // #Node的 排序等级
		target  *TNode // 记录等级的Node 一般为/ 开始的第一个动态
		node    *TNode
	)
	// 默认
	nodes = make([]*TNode, 0)
	isDyn = false
	l := len(path)
	//j = i - 1 // 当i==0时J必须小于它
	for ; i < l; i++ {
		switch path[i] {
		case delimitChar:
			{ // 创建Text:'/' Node
				if bracket == 0 && i > startOffset {
					//if path[j] == '/' {
					//	nodes = append(nodes, &TNode{Type: StaticNode, Text: string(path[j])})
					//}
					//j++
					nodes = append(nodes, &TNode{Type: StaticNode, Level: 0, Text: path[startOffset:i]})
					startOffset = i
				}

				//fmt.Println("/")
				// # 重置计数
				target = nil
				level = 0 // #开始计数
			}
		case '(':
			{
				//fmt.Println("(")
				bracket = 1
			}
		case ':':
			{
				//fmt.Println(":", bracket, path[j:i-bracket])
				var typ ContentType = AllType
				if path[i-1] == '(' { //#like (:var)
					nodes = append(nodes, &TNode{Type: StaticNode, Text: path[startOffset : i-bracket]})
					bracket = 1
				} else {
					// #为变量区分数据类型
					str := path[startOffset : i-bracket] // #like /abc1(string|upper:var)
					idx := strings.Index(str, "(")
					if idx == -1 {
						panic(fmt.Sprintf("expect a '(' near position %d~%d", startOffset, i))
					}
					nodes = append(nodes, &TNode{Type: StaticNode, Text: str[:idx]})
					str = str[idx+1:]
					switch str {
					case "int":
						typ = NumberType
					case "string":
						typ = CharType
					default:
						typ = AllType
					}
					//fmt.Println("type:", typ)
					bracket = 1
				}

				startOffset = i
				var (
					regex string
					start = -1
				)

				if bracket == 1 {
					// 开始记录Pos
					for ; i < l && ')' != path[i]; i++ { // 移动Pos到")" 遇到正则字符标记起
						if start == -1 && utils.IsSpecialByte(path[i]) { // 如果是正则
							start = i
						}
					}
					if path[i] != ')' {
						panic("lack of )")
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
					node = &TNode{Type: RegexpNode, regexp: regexp.MustCompile("(" + regex + ")"), Text: path[startOffset : i-len(regex)]}
					nodes = append(nodes, node)
				} else { // 变量
					node = &TNode{Type: VariantNode, Level: level + 1, ContentType: typ, Text: path[startOffset:i]}
					nodes = append(nodes, node)
				}

				isDyn = true    // #标记 Route 为动态
				i = i + bracket // #剔除")"字符 bracket=len(“)”)
				startOffset = i

				// 当计数器遇到/或者Url末尾时将记录保存于Node中
				if target != nil && ((i == l) || (i != l && path[startOffset+1] == delimitChar)) {
					level++
					target.Level = level
					//fmt.Println("ok:", node.Text, target.Text, level)

					// # 重置计数
					target = nil
					level = 0
				}

				if i == l {
					return //nodes, isDyn
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
					//fmt.Println("leve:", node.Text, target.Text, level)
				}
			}
		case '*':
			{
				nodes = append(nodes, &TNode{Type: StaticNode, Text: path[startOffset : i-bracket]})
				startOffset = i
				//if bracket == 1 {
				//	for ; i < l && ')' == path[i]; i++ {
				//	}
				//} else {
				i = i + 1
				for ; i < l && utils.IsAlnumByte(path[i]); i++ {
				}
				//}
				nodes = append(nodes, &TNode{Type: AnyNode, Level: -1, Text: path[startOffset:i]})
				isDyn = true    // 标记 Route 为动态
				i = i + bracket // bracket=len(“)”)
				startOffset = i
				if i == l {
					return //nodes, isDyn
				}
			}

		default:
			{
				bracket = 0
			}
		}
	}

	nodes = append(nodes, &TNode{
		Type: StaticNode,
		Text: path[startOffset:i],
	})

	return //nodes, isDyn
}

func (r *TTree) matchNode(node *TNode, path string, delimitChar byte, aParams *Params) *TNode {
	var retnil bool
	if node.Type == StaticNode { // 静态节点
		// match static node
		if strings.HasPrefix(path, node.Text) {
			//fmt.Println("J态", path, " | ", node.Text)
			if len(path) == len(node.Text) {
				return node
			}

			for _, c := range node.Children {
				e := r.matchNode(c, path[len(node.Text):], delimitChar, aParams)
				if e != nil {
					return e
				}
			}
		}

	} else if node.Type == AnyNode { // 全匹配节点
		//if len(node.Children) == 0 {
		//	*aParams = append(*aParams, param{node.Text[1:], path})
		//	return node
		//}
		//fmt.Println("Any态", path, " | ", node.Text)
		for _, c := range node.Children {
			idx := strings.LastIndex(path, c.Text)
			//fmt.Println("LastIndex", path, c.Text)
			if idx > -1 {
				h := r.matchNode(c, path[idx:], delimitChar, aParams)
				if h != nil {
					*aParams = append(*aParams, param{node.Text[1:], path[:idx]})
					return h
				}

			}
			//fmt.Println("Any态2", path, idx, c.Text[1:])

		}

		*aParams = append(*aParams, param{node.Text[1:], path})
		return node
	} else if node.Type == VariantNode { // 变量节点
		// # 消除path like /abc 的'/'
		// 根据首字符判断接下来的处理条件
		first_Char := path[0]
		//idx := strings.IndexByte(path, delimitChar)
		//fmt.Println("D态", path, string(delimitChar), " | ", first_Char)
		if first_Char == delimitChar { // #fix错误if idx > -1 {
			for _, c := range node.Children {
				h := r.matchNode(c, path[0:], delimitChar, aParams)
				if h != nil {
					/*fmt.Println("类型1", path[:idx], node.ContentType)
					if !validType(path[:idx], node.ContentType) {
						fmt.Println("错误类型", path[:idx], node.ContentType)
						return nil
					}
					*/
					*aParams = append(*aParams, param{node.Text[1:], path[:0]})
					return h
				}
			}
			return nil
		}

		if len(node.Children) == 0 { // !NOTE! 匹配到最后一个条件
			// !NOTE! 防止过度匹配带分隔符的字符
			idx := strings.IndexByte(path, delimitChar)
			if idx > -1 {
				return nil
			}

			//fmt.Println("动态", path, node.Text[1:])
			*aParams = append(*aParams, param{node.Text[1:], path})
			return node
		} else { // !NOTE! 匹配回溯 当匹配进入错误子节点返回nil到父节点重新匹配父节点
			//fmt.Println("Children", len(node.Children))
			for _, c := range node.Children {
				idx := strings.Index(path, c.Text) // #匹配前面检索到的/之前的字符串
				if idx > -1 {
					//fmt.Println("Index", idx, path, c.Text, path[:idx])

					if len(path[:idx]) > 1 && strings.IndexByte(path[:idx], delimitChar) > -1 {
						retnil = true
						continue
					}

					//fmt.Println("类型2", path[:idx], contentType[node.ContentType], contentType[c.ContentType])
					if !validType(path[:idx], node.ContentType) {
						continue
					}

					//fmt.Println("类型3", path[idx:], node.Text[1:], path[:idx])
					h := r.matchNode(c, path[idx:], delimitChar, aParams)
					if h != nil {
						*aParams = append(*aParams, param{node.Text[1:], path[:idx]})
						return h
					}
					retnil = true
				}
			}

			//fmt.Println("动态a", retnil, len(node.Children))
			if retnil || len(node.Children) > 0 {
				return nil
			}
		}
	} else if node.Type == RegexpNode { // 正则节点
		//if len(node.Children) == 0 && node.regexp.MatchString(path) {
		//	*aParams = append(*aParams, param{node.Text[1:], path})
		//	return node
		//}
		idx := strings.IndexByte(path, delimitChar)
		if idx > -1 {
			if node.regexp.MatchString(path[:idx]) {
				for _, c := range node.Children {
					h := r.matchNode(c, path[idx:], delimitChar, aParams)
					if h != nil {
						*aParams = append(*aParams, param{node.Text[1:], path[:idx]})
						return h
					}
				}
			}
			return nil
		}
		for _, c := range node.Children {
			idx := strings.Index(path, c.Text)
			if idx > -1 && node.regexp.MatchString(path[:idx]) {
				h := r.matchNode(c, path[idx:], delimitChar, aParams)
				if h != nil {
					*aParams = append(*aParams, param{node.Text[1:], path[:idx]})
					return h
				}

			}
		}

		if node.regexp.MatchString(path) {
			*aParams = append(*aParams, param{node.Text[1:], path})
			return node
		}

	}

	return nil
}

func (r *TTree) Match(method string, path string) (*TRoute, Params) {
	delimitChar := r.DelimitChar
	if delimitChar == 0 {
		delimitChar = '/'
	}

	root := r.root[method]
	if root != nil {
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
func validNodes(nodes []*TNode) bool {
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

// 添加路由到Tree
func (self *TTree) AddRoute(method, path string, route *TRoute) {
	delimitChar := self.DelimitChar
	if delimitChar == 0 {
		delimitChar = '/'
	}

	// to parse path as a List node
	nodes, is_dyn := self.parsePath(path, delimitChar)

	// marked as a dynamic route
	route.isDynRoute = is_dyn // 即将Hook的新Route是动态地址

	// 绑定Route到最后一个Node
	node := nodes[len(nodes)-1]
	route.Action = node.Text // 赋值Action
	route.Path = path        // 存储路由绑定的URL
	node.Route = route
	node.Path = path

	// 验证合法性
	if !validNodes(nodes) {
		logger.Panicf("express %s is not supported", path)
	}

	// insert the node to tree
	self.addNodes(method, nodes, false)
}

// delete the route
func (self *TTree) DeleteRoute(method, path string) {

}

// conbine 2 node together
func (self *TTree) conbine(target, from *TNode) {
	var exist_node *TNode

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
				exist_node.Route.CombineController(from.Route)
			}
		}

		// conbine sub-node
		for _, n := range from.Children {
			self.conbine(exist_node, n)
		}
	}
}

// conbine 2 tree together
func (self *TTree) Conbine(from *TTree) *TTree {
	// !NOTE! 避免合并不同分隔符的路由树
	if len(self.root) > 0 && len(from.root) > 0 { // 非空的Tree
		if self.DelimitChar != from.DelimitChar { // 分隔符对比
			logger.Panicf("could not conbine 2 different kinds (RPC/HTTP) of routes tree!")
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

// add node nodes[i] to parent node p
func (self *TNode) addNode(parent *TNode, nodes []*TNode, i int, isHook bool) *TNode {
	if len(parent.Children) == 0 {
		parent.Children = make([]*TNode, 0)
	}

	// 如果:找到[已经注册]的分支节点则从该节继续[查找/添加]下一个节点
	for _, n := range parent.Children {
		if n.Equal(nodes[i]) {
			// 如果:插入的节点层级已经到末尾,则为该节点注册路由
			if i == len(nodes)-1 {
				// 原始路由会被替换
				if isHook {
					n.Route.CombineController(nodes[i].Route)
				} else {
					n.Route = nodes[i].Route
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

// add nodes to trees
func (self *TTree) addNodes(method string, nodes []*TNode, isHook bool) {
	//fmt.Println("self.Root", self.Root)
	// 获得对应方法[POST,GET...]
	cn := self.root[method]
	if cn == nil {

		// 初始化Root node
		cn = &TNode{
			Children: TSubNodes{},
		}
		self.root[method] = cn
	}

	var p *TNode = cn // 复制方法对应的Root

	// 层级插入Nodes的Node到Root
	for idx := range nodes {
		p = cn.addNode(p, nodes, idx, isHook)
	}
}

func printNode(i int, node *TNode) {
	for _, c := range node.Children {
		for j := 0; j < i; j++ { // 空格距离ss
			fmt.Print("  ")
		}
		if i > 1 {
			fmt.Print("┗", "  ")
		}

		fmt.Printf(`%s <Lv:%d; Type:%v; VarType:%v>`, c.Text, c.Level, nodeType[c.Type], contentType[c.ContentType])
		if c.Route != nil {
			fmt.Print(" <*>")
		}
		//if !reflect.DeepEqual(c.Route, TRoute{}) {
		if c.Route != nil {
			//fmt.Print("  ", c.Route.HandleType.String())
			//fmt.Printf("  %p", c.handle.method.Interface())
		}
		fmt.Println()
		printNode(i+1, c)
	}
}

func (self *TTree) PrintTrees() {
	for method, node := range self.root {
		if len(node.Children) > 0 {
			fmt.Println(method)
			printNode(1, node)
			fmt.Println()
		}
	}
}

func (self *TNode) Equal(node *TNode) bool {
	if self.Type != node.Type || self.Text != node.Text || self.ContentType != node.ContentType {
		return false
	}
	return true
}
