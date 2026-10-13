// Package shapes is a code graph fixture.
package shapes

// Shape is anything with an area.
type Shape interface {
	Area() float64
}

type Square struct{ Side float64 }

// Area of a square.
func (s Square) Area() float64 { return s.Side * s.Side }

type Circle struct{ R float64 }

func (c *Circle) Area() float64 { return Pi * c.R * c.R }

// Total adds the areas through the interface.
func Total(list []Shape) float64 {
	t := 0.0
	for _, s := range list {
		t += s.Area()
	}
	return t
}

const Pi = 3

var Default = NewSquare(1)

func NewSquare(side float64) Square { return Square{Side: side} }
