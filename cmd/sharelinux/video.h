//go:build linux

#ifndef OSS_VIDEO_H
#define OSS_VIDEO_H

#include <gtk/gtk.h>

// The Mac's HEVC (or H.264) media stream, decoded with libavcodec. The decoder
// is VA-API (a GPU surface, handed to GTK as a DMA-BUF) or software; the
// widget (screen.c) shows the textures it makes. Nothing here touches GTK
// objects other than the textures, so the decoder runs on its own thread.

typedef enum {
	OSS_VIDEO_HEVC = 0,
	OSS_VIDEO_H264 = 1,
} OssVideoCodec;

typedef enum {
	OSS_DECODER_NONE = 0,
	OSS_DECODER_VAAPI = 1,
	OSS_DECODER_SOFTWARE = 2,
} OssDecoderKind;

// How a frame reaches the screen.
typedef enum {
	OSS_OUTPUT_NONE = 0,
	OSS_OUTPUT_DMABUF = 1, // the VA surface itself, exported as a DMA-BUF
	OSS_OUTPUT_COPY = 2,   // read back from the GPU into a memory texture
	OSS_OUTPUT_MEMORY = 3, // decoded in software into a memory texture
} OssOutput;

// Return codes of oss_video_decode below zero.
#define OSS_ERR_BAD_DATA (-1) // this unit failed: wait for a keyframe
#define OSS_ERR_FATAL (-2)    // the decoder is unusable: use another one

// OssVideoProbe says which decoders this machine has.
typedef struct {
	gboolean vaapi[2];    // indexed by OssVideoCodec
	gboolean software[2]; // indexed by OssVideoCodec
	char device[96];      // the VA-API render node asked for, empty for the first one
	char driver[96];      // what the VA-API driver says it is
	char error[192];      // why VA-API is missing, empty when it works
} OssVideoProbe;

// oss_video_probe opens VA-API on device (NULL: the first render node) and
// asks libavcodec for the software decoders. It is quick and has no effects.
void oss_video_probe(OssVideoProbe *out, const char *device);

// OssColor is what the stream's sequence header says about its colors.
typedef struct {
	int primaries, transfer, matrix; // ISO/IEC 23091-2 code points, 2 is unspecified
	gboolean full_range;
	int bit_depth, chroma_format;
} OssColor;

typedef struct OssVideoDecoder OssVideoDecoder;
typedef struct OssFrame OssFrame;

// oss_video_open makes a decoder. The parameter sets (VPS, SPS, PPS or SPS,
// PPS; no start codes) are packed one after another in sets, set_lens gives each
// length. On failure it returns NULL and fills err.
OssVideoDecoder *oss_video_open(OssVideoCodec codec, OssDecoderKind kind, const char *device, int threads, const guint8 *sets,
                                const int *set_lens, int set_count, OssColor color, char *err, int err_len);

// oss_video_close frees the decoder and every frame it still holds.
void oss_video_close(OssVideoDecoder *decoder);

// oss_video_kind says which decoder it really is.
OssDecoderKind oss_video_kind(const OssVideoDecoder *decoder);

// oss_video_decode feeds one access unit in the form the Mac sends it: NAL
// units, each behind a four byte big endian length. age_us is how long ago
// the unit's first packet arrived. sync marks a keyframe. It returns the
// number of frames now waiting for oss_video_next, or a negative error.
int oss_video_decode(OssVideoDecoder *decoder, const guint8 *data, gsize length, gboolean sync, gint64 age_us);

// oss_video_next takes the next decoded frame, or NULL.
OssFrame *oss_video_next(OssVideoDecoder *decoder);

// oss_video_flush drops the pictures in flight; the next unit must be a keyframe.
void oss_video_flush(OssVideoDecoder *decoder);

typedef struct {
	int width, height;     // the visible picture
	int tex_width, tex_height; // the surface or buffer behind it
	gboolean hardware;
	gint64 received_us;    // g_get_monotonic_time when the unit's first packet arrived
	gint64 decoded_us;     // when the frame was ready
	int decode_us;         // from the unit going in to the frame being ready
	gsize unit_bytes;      // size of the access unit it came from
	OssColor color;
} OssFrameInfo;

void oss_frame_info(const OssFrame *frame, OssFrameInfo *info);

// oss_frame_free drops a frame; it may be called on any thread.
void oss_frame_free(OssFrame *frame);

// oss_frame_luma_mean is the average luma of a software frame, for tests; -1 for a GPU frame.
double oss_frame_luma_mean(const OssFrame *frame);

// oss_frame_texture makes a texture from a frame. With dmabuf set, a GPU frame
// goes to GTK as a DMA-BUF when the display can import it; otherwise (or
// when that fails) the picture is read back into a memory texture. The frame
// stays valid: the texture holds its own reference to a GPU surface. output
// says which way it went.
GdkTexture *oss_frame_texture(const OssFrame *frame, GdkDisplay *display, gboolean dmabuf, OssOutput *output);

// oss_dmabuf_nv12_supported says whether the display imports NV12 DMA-BUFs at all.
gboolean oss_dmabuf_nv12_supported(GdkDisplay *display);

// OssVideoStats are the decoder's running counters.
typedef struct {
	guint64 units;     // access units given in
	guint64 frames;    // frames out
	guint64 errors;    // units the decoder refused
	guint64 flushes;
	guint64 decode_us_total;
	int decode_us_last, decode_us_max;
} OssVideoStats;

void oss_video_stats(const OssVideoDecoder *decoder, OssVideoStats *stats);

// OssVideoLive counts what is alive right now, process wide. After a clean
// teardown every field is zero.
typedef struct {
	int decoders, frames, av_frames, fds, textures;
} OssVideoLive;

void oss_video_live(OssVideoLive *live);

// oss_video_test_break_dmabuf makes every DMA-BUF import fail at the builder, for tests of the
// failure path.
void oss_video_test_break_dmabuf(gboolean broken);

// oss_color_state builds the GdkColorState for a stream. Values GTK cannot use
// become BT.709 limited range.
GdkColorState *oss_color_state(OssColor color);

#endif
