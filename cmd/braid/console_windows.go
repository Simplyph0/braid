package main

import "syscall"

// detachConsole closes the console window Windows opens for a program
// started at sign-in.
func detachConsole() { syscall.NewLazyDLL("kernel32.dll").NewProc("FreeConsole").Call() }
