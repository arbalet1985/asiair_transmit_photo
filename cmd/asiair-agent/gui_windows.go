package main

import "github.com/arbalet1985/asiair_transmit_photo/internal/gui"

// The .syso resource files next to this file carry the application icon and
// the manifest the window needs (common controls v6, DPI awareness).
//go:generate go run github.com/tc-hib/go-winres@v0.3.3 simply --icon ../../internal/gui/agent-icon.png --manifest gui --product-name "ASIAIR Agent" --file-description "ASIAIR Agent"

var guiMain = gui.Run
