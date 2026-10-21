//go:build linux

package main

import (
	"testing"

	"github.com/nekohasekai/open-screen-sharing/internal/rfb/media"
)

func TestHDRColorStates(t *testing.T) {
	tests := []struct {
		name     string
		color    media.Color
		space    colorSpace
		wantCICP [4]int
		wantPQ   bool
		wantOK   bool
	}{
		{
			name: "PQ BT.2020 YUV444",
			color: media.Color{
				Primaries:    9,
				Transfer:     16,
				Matrix:       9,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceYUV444,
			wantCICP: [4]int{9, 16, 9, 1},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			name: "PQ BT.2020 RGB",
			color: media.Color{
				Primaries:    9,
				Transfer:     16,
				Matrix:       9,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceRGB,
			wantCICP: [4]int{9, 16, 0, 1},
			wantPQ:   true,
			wantOK:   true,
		},
		{
			name: "HLG BT.2020 YUV444",
			color: media.Color{
				Primaries:    9,
				Transfer:     18,
				Matrix:       9,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceYUV444,
			wantCICP: [4]int{9, 18, 9, 1},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			name: "HLG BT.2020 RGB",
			color: media.Color{
				Primaries:    9,
				Transfer:     18,
				Matrix:       9,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceRGB,
			wantCICP: [4]int{9, 18, 0, 1},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			name: "PQ unspecified matrix YUV444",
			color: media.Color{
				Primaries:    9,
				Transfer:     16,
				Matrix:       2,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceYUV444,
			wantCICP: [4]int{9, 16, 9, 1},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			name: "PQ BT.709 unspecified matrix YUV444",
			color: media.Color{
				Primaries:    1,
				Transfer:     16,
				Matrix:       2,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceYUV444,
			wantCICP: [4]int{1, 16, 1, 1},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			// Matrix 0 is the identity: the planes are G, B and R, so no YUV state describes them.
			name: "PQ identity matrix (GBR) has no YUV state",
			color: media.Color{
				Primaries:    9,
				Transfer:     16,
				Matrix:       0,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:  spaceYUV444,
			wantOK: false,
		},
		{
			// The RGB of a GBR stream is described by primaries and transfer alone.
			name: "PQ identity matrix (GBR) RGB",
			color: media.Color{
				Primaries:    9,
				Transfer:     16,
				Matrix:       0,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceRGB,
			wantCICP: [4]int{9, 16, 0, 1},
			wantPQ:   true,
			wantOK:   true,
		},
		{
			// What the Mac sends: Display P3 primaries, BT.709 matrix, full range, PQ.
			name: "PQ Display P3 BT.709 matrix YUV444",
			color: media.Color{
				Primaries:    12,
				Transfer:     16,
				Matrix:       1,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceYUV444,
			wantCICP: [4]int{12, 16, 1, 1},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			name: "PQ Display P3 RGB",
			color: media.Color{
				Primaries:    12,
				Transfer:     16,
				Matrix:       1,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceRGB,
			wantCICP: [4]int{12, 16, 0, 1},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			name: "unspecified HDR color",
			color: media.Color{
				Primaries:    2,
				Transfer:     2,
				Matrix:       2,
				FullRange:    true,
				BitDepth:     10,
				ChromaFormat: 3,
			},
			space:    spaceYUV444,
			wantCICP: [4]int{0, 0, 0, 0},
			wantPQ:   false,
			wantOK:   false,
		},
		{
			name: "SDR BT.709 YUV",
			color: media.Color{
				Primaries:    1,
				Transfer:     13,
				Matrix:       1,
				FullRange:    false,
				BitDepth:     8,
				ChromaFormat: 1,
			},
			space:    spaceYUV,
			wantCICP: [4]int{1, 13, 1, 0},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			name: "SDR BT.709 RGB",
			color: media.Color{
				Primaries:    1,
				Transfer:     13,
				Matrix:       1,
				FullRange:    false,
				BitDepth:     8,
				ChromaFormat: 1,
			},
			space:    spaceRGB,
			wantCICP: [4]int{1, 13, 0, 1},
			wantPQ:   false,
			wantOK:   true,
		},
		{
			name: "unspecified SDR fallback",
			color: media.Color{
				Primaries:    2,
				Transfer:     2,
				Matrix:       2,
				FullRange:    false,
				BitDepth:     8,
				ChromaFormat: 1,
			},
			space:    spaceYUV,
			wantCICP: [4]int{1, 13, 1, 0},
			wantPQ:   false,
			wantOK:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotCICP, gotPQ, gotOK := colorStateForTest(tt.color, tt.space)
			if gotCICP != tt.wantCICP || gotPQ != tt.wantPQ || gotOK != tt.wantOK {
				t.Fatalf(
					"colorStateForTest(%+v, %v) = (%v, %t, %t), want (%v, %t, %t)",
					tt.color,
					tt.space,
					gotCICP,
					gotPQ,
					gotOK,
					tt.wantCICP,
					tt.wantPQ,
					tt.wantOK,
				)
			}
		})
	}
}
