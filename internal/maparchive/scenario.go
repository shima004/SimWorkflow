package maparchive

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"strings"
)

// Counts use the Java launcher's order: FB, FS, PF, PO, AT, AC.
func ScenarioCounts(path string) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	counts := make([]int, 6)
	decoder := xml.NewDecoder(f)
	indices := map[string]int{"firebrigade": 0, "firestation": 1, "policeforce": 2, "policeoffice": 3, "ambulanceteam": 4, "ambulancecentre": 5, "ambulancecenter": 5}
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, errors.New("invalid scenario XML")
		}
		if element, ok := token.(xml.StartElement); ok {
			if index, exists := indices[strings.ToLower(element.Name.Local)]; exists {
				counts[index]++
			}
		}
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	if total == 0 {
		return nil, errors.New("scenario has no controllable agents")
	}
	return counts, nil
}
