package main

import (
	"reflect"
	"testing"
)

func TestParsePorts(t *testing.T) {
	got := parsePorts("80, 443 ,8123,,9200,notaport,8080")
	want := []int{80, 443, 8123, 9200, 8080}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePorts = %v, want %v (trims spaces, skips empties and non-numbers)", got, want)
	}
}
