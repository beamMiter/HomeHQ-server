package medicine

import "errors"

var ErrNotFound = errors.New("not found")
var ErrBadInput = errors.New("bad input")

// ErrInsufficientStock: taking out more than there is.
var ErrInsufficientStock = errors.New("insufficient stock")