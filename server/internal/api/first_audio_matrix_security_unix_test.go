//go:build !windows

package api

func verifyFirstAudioPrivateDirectory(_ string) error { return nil }

func createFirstAudioPrivateTestDirectory(_ string) error { return nil }
