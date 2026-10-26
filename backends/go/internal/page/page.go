// Package page bounds the responses that list things, so that every frame
// from the server stays within the contract's 1 MiB.
package page

// MaxBytes is how much a page may hold before it ends early; with the
// largest single item (a message of 48 KiB with its reactions) on top, the
// frame stays well within 1 MiB.
const MaxBytes = 512 << 10
