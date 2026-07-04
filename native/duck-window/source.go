package duckwindow

import _ "embed"

// MainSwift is the native Duck Window AppKit/WKWebView backend source.
//
//go:embed main.swift
var MainSwift string
