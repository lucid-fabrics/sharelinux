package main

import (
	"os"
	"strings"
	"testing"
)

// minimumGTK is the oldest GTK the code builds and runs with: the color states and the memory texture builder are 4.16.
const minimumGTK = "4.16"

// The README names the GTK the code needs.
func TestReadmeNamesTheGTKTheCodeNeeds(t *testing.T) {
	text, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Skip(err)
	}
	if !strings.Contains(string(text), "GTK **"+minimumGTK+"** or newer") {
		t.Fatalf("README should say GTK %s or newer", minimumGTK)
	}
	if strings.Contains(string(text), "GTK **4.14**") {
		t.Fatal("README still says 4.14")
	}
}
