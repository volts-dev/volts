package server

import (
	"fmt"
	"testing"
)

func TestTree(t *testing.T) {
	tree1 := NewRouteTree()
	tree2 := NewRouteTree()
	r := new(TRoute)

	fmt.Println("Test combine")
	tree1.AddRoute("GET", "/abc1", r)
	tree2.AddRoute("GET", "/abc1/abc2", r)

	tree1.AddRoute("GET", "/abc1/tree1", r)
	tree2.AddRoute("GET", "/abc1/tree2", r)

	tree1.AddRoute("GET", "/abc1/(:nameA)abc1(:nameB)", r)
	tree2.AddRoute("GET", "/abc1/(:nameA)abc1(:nameB)", r)
	tree1.Conbine(tree2)
	//	tree1.printTrees()
	//	tree2.printTrees()

	fmt.Println("Test macth")
	//tree1.AddRoute("GET", "/abc1/(:name1)abc1(:name2)", r)

	tree1.AddRoute("GET", "/abc1(string:name1)abc1(:name2)abc1", r)
	tree1.AddRoute("GET", "/web/content/(string:modelA)/(:id)/(:field)", r)
	tree1.AddRoute("GET", "/web/content/(:modelB)/(:id)/(:field)/(:filename)", r)
	tree1.AddRoute("GET", "/web/content/(int:id1)-(string:unique2)", r)
	tree1.AddRoute("GET", "/web/content/(int:id3)-(int:unique3)/(:filename)", r)
	tree1.AddRoute("GET", "/web/content/(:id)", r)
	tree1.AddRoute("GET", "/web/content/(:id)/(:filename)", r)
	tree1.AddRoute("GET", "/web/content/(:xmlid)", r)
	tree1.AddRoute("GET", "/web/content/(:xmlid)/(:filename)", r)
	tree1.AddRoute("GET", "/(:name1)abc1(:name2)", r)
	tree1.AddRoute("GET", "/(:name1)abc1(:name2)abc1", r)
	tree1.AddRoute("GET", "/(*name1)", r)
	tree1.AddRoute("GET", "/(*name1)abc1", r)
	tree1.PrintTrees()

	r, p := tree1.Match("GET", "/abc1/abc1/abc1")
	if r != nil {
		fmt.Println("/abc1/abc1/abc1", r.Path, p)
	}
	r, p = tree1.Match("GET", "/web/content/36-0420888/website.assets_frontend.0.css")
	fmt.Println("/web/content/36-0420888/website.assets_frontend.0.css", r, p)
	r, p = tree1.Match("GET", "/abc1abc4abc1dfabc1")
	fmt.Println("/abc1abc4abc1dfabc1", r, p)
	r, p = tree1.Match("GET", "/adffabc1ab/c4abc1abc1")
	fmt.Println("/adffabc1ab/c4abc1abc1", r.Path, p)

}
