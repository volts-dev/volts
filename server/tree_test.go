package server

import (
	"fmt"
	"testing"
)

func TestTree(t *testing.T) {
	tree := NewRouteTree()
	tree2 := NewRouteTree()
	r := new(TRoute)

	fmt.Println("Test combine")
	tree.AddRoute("GET", "/abc1/abc1/abc1", r)
	tree2.AddRoute("GET", "/abc1/(:name1)abc1(:name2)", r)
	//tree.Conbine(tree2)
	//	tree.printTrees()
	//	tree2.printTrees()

	fmt.Println("Test macth")
	//tree.AddRoute("GET", "/abc1/(:name1)abc1(:name2)", r)

	tree.AddRoute("GET", "/abc1(string:name1)abc1(:name2)abc1", r)
	tree.AddRoute("GET", "/web/content/(string:modelA)/(:id)/(:field)", r)
	tree.AddRoute("GET", "/web/content/(:modelB)/(:id)/(:field)/(:filename)", r)
	tree.AddRoute("GET", "/web/content/(int:id1)-(string:unique2)", r)
	tree.AddRoute("GET", "/web/content/(int:id3)-(int:unique3)/(:filename)", r)
	tree.AddRoute("GET", "/web/content/(:id)", r)
	tree.AddRoute("GET", "/web/content/(:id)/(:filename)", r)
	tree.AddRoute("GET", "/web/content/(:xmlid)", r)
	tree.AddRoute("GET", "/web/content/(:xmlid)/(:filename)", r)
	tree.AddRoute("GET", "/(:name1)abc1(:name2)", r)
	tree.AddRoute("GET", "/(:name1)abc1(:name2)abc1", r)
	tree.AddRoute("GET", "/(*name1)", r)
	tree.AddRoute("GET", "/(*name1)abc1", r)
	tree.PrintTrees()

	r, p := tree.Match("GET", "/abc1/abc1/abc1")
	fmt.Println("/abc1/abc1/abc1", r.Path, p)
	r, p = tree.Match("GET", "/web/content/36-0420888/website.assets_frontend.0.css")
	fmt.Println("/web/content/36-0420888/website.assets_frontend.0.css", r, p)
	r, p = tree.Match("GET", "/abc1abc4abc1dfabc1")
	fmt.Println("/abc1abc4abc1dfabc1", r, p)
	r, p = tree.Match("GET", "/adffabc1ab/c4abc1abc1")
	fmt.Println("/adffabc1ab/c4abc1abc1", r.Path, p)

}
