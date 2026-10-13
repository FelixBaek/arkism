package report

import "example.com/fixture/shapes"

func Summary() float64 {
	return shapes.Total([]shapes.Shape{shapes.NewSquare(2), &shapes.Circle{R: 1}})
}

func Twice() float64 { return Summary() * 2 }

func Top() float64 {
	f := Twice
	return f() + helper()
}

func helper() float64 { return 0 }
