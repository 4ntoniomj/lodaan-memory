package main

import (
	"fmt"
	"os"
)

func main() {
	for _, f := range os.Args[1:] {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Printf("%s: %d bytes, starts with %d\n", f, len(b), b[0])
		if b[0] == 239 {
			fmt.Println("  has BOM")
		}
		// Check for double-encoded UTF-8: C3 83 22 (= "Ã\"") and C3 83 C2 (bad mojibake)
		count83 := 0
		countAD := 0
		for i := 0; i < len(b)-1; i++ {
			if b[i] == 195 && b[i+1] == 131 {
				count83++
			}
		}
		for i := 0; i < len(b)-1; i++ {
			if b[i] == 195 && b[i+1] == 161 {
				countAD++
			}
		}
		fmt.Printf("  C3 83 count: %d  (double-encoded mojibake)\n", count83)
		fmt.Printf("  C3 A1 count: %d  (proper UTF-8 'á')\n", countAD)
	}
}