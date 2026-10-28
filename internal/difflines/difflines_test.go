package difflines

import "testing"

const sample = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,4 +1,5 @@
 package main
+import "fmt"
 func main() {
-	println("hi")
+	fmt.Println("hi")
 }
@@ -20,3 +21,3 @@ func other() {
 	a := 1
-	b := 2
+	b := 3
 	_ = a + b
diff --git a/new.go b/new.go
new file mode 100644
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package main
+var x = 1
diff --git a/gone.go b/gone.go
deleted file mode 100644
--- a/gone.go
+++ /dev/null
@@ -1,2 +0,0 @@
-package main
-var y = 2
diff --git "a/sp ace.go" "b/sp ace.go"
--- "a/sp ace.go"
+++ "b/sp ace.go"
@@ -3 +3 @@
-old
+new
diff --git a/img.png b/img.png
Binary files a/img.png and b/img.png differ
`

func TestCommentable(t *testing.T) {
	m := Parse(sample)
	cases := []struct {
		path string
		line int
		want bool
	}{
		{"main.go", 1, true}, {"main.go", 2, true}, {"main.go", 5, true}, // first hunk: new lines 1-5
		{"main.go", 6, false}, {"main.go", 20, false}, // between hunks
		{"main.go", 21, true}, {"main.go", 23, true}, {"main.go", 24, false}, // second hunk: 21-23
		{"new.go", 1, true}, {"new.go", 2, true}, {"new.go", 3, false},
		{"gone.go", 1, false},                           // deleted: nothing on the new side
		{"sp ace.go", 3, true}, {"sp ace.go", 4, false}, // quoted path, count omitted
		{"img.png", 1, false},
		{"other.go", 1, false},
	}
	for _, tc := range cases {
		if got := m.Has(tc.path, tc.line); got != tc.want {
			t.Errorf("Has(%q, %d) = %v, want %v", tc.path, tc.line, got, tc.want)
		}
	}
}

func TestEmptyDiff(t *testing.T) {
	if Parse("").Has("a.go", 1) {
		t.Error("an empty diff has no commentable lines")
	}
}
