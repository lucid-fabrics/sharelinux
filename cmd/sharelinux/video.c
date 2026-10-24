//go:build linux

#include "video.h"

#include <libavcodec/avcodec.h>
#include <libavutil/error.h>
#include <libavutil/hwcontext.h>
#include <libavutil/hwcontext_vaapi.h>
#include <libavutil/imgutils.h>
#include <libavutil/pixdesc.h>
#include <libswscale/swscale.h>
#include <string.h>
#include <unistd.h>
#include <va/va.h>
#include <va/va_drmcommon.h>

#define FOURCC(a, b, c, d) ((guint32)(a) | ((guint32)(b) << 8) | ((guint32)(c) << 16) | ((guint32)(d) << 24))
#define DRM_FORMAT_NV12 FOURCC('N', 'V', '1', '2')
#define DRM_FORMAT_Y410 FOURCC('Y', '4', '1', '0')
#define DRM_FORMAT_AYUV FOURCC('A', 'Y', 'U', 'V')
#define DRM_FORMAT_XYUV8888 FOURCC('X', 'Y', 'U', 'V')
#define DRM_FORMAT_XRGB8888 FOURCC('X', 'R', '2', '4')
#define DRM_FORMAT_XRGB2101010 FOURCC('X', 'R', '3', '0')
#define DRM_FORMAT_ARGB2101010 FOURCC('A', 'R', '3', '0')

// What is alive, process wide, for the leak checks.
static gint live_decoders, live_frames, live_av_frames, live_fds, live_textures;

void oss_video_live(OssVideoLive *live) {
	live->decoders = g_atomic_int_get(&live_decoders);
	live->frames = g_atomic_int_get(&live_frames);
	live->av_frames = g_atomic_int_get(&live_av_frames);
	live->fds = g_atomic_int_get(&live_fds);
	live->textures = g_atomic_int_get(&live_textures);
}

static AVFrame *frame_alloc(void) {
	AVFrame *frame = av_frame_alloc();
	if (frame != NULL) {
		g_atomic_int_inc(&live_av_frames);
	}
	return frame;
}

static void frame_release(AVFrame **frame) {
	if (*frame != NULL) {
		av_frame_free(frame);
		g_atomic_int_dec_and_test(&live_av_frames);
	}
}

static AVFrame *frame_ref(const AVFrame *source) {
	AVFrame *frame = av_frame_clone(source);
	if (frame != NULL) {
		g_atomic_int_inc(&live_av_frames);
	}
	return frame;
}

// ---- probing -------------------------------------------------------------

static gboolean vaapi_has_profile(VADisplay display, VAProfile profile) {
	VAEntrypoint entrypoints[16];
	int count = 0;
	if (vaQueryConfigEntrypoints(display, profile, entrypoints, &count) != VA_STATUS_SUCCESS) {
		return FALSE;
	}
	for (int i = 0; i < count; i++) {
		if (entrypoints[i] == VAEntrypointVLD) {
			return TRUE;
		}
	}
	return FALSE;
}

void oss_video_probe(OssVideoProbe *out, const char *device) {
	memset(out, 0, sizeof *out);
	out->software[OSS_VIDEO_HEVC] = avcodec_find_decoder(AV_CODEC_ID_HEVC) != NULL;
	out->software[OSS_VIDEO_H264] = avcodec_find_decoder(AV_CODEC_ID_H264) != NULL;
	if (device != NULL && device[0] == '\0') {
		device = NULL;
	}
	AVBufferRef *context = NULL;
	int result = av_hwdevice_ctx_create(&context, AV_HWDEVICE_TYPE_VAAPI, device, NULL, 0);
	if (result < 0) {
		char text[AV_ERROR_MAX_STRING_SIZE];
		av_strerror(result, text, sizeof text);
		g_snprintf(out->error, sizeof out->error, "VA-API: %s", text);
		return;
	}
	g_strlcpy(out->device, device != NULL ? device : "", sizeof out->device);
	AVHWDeviceContext *hw = (AVHWDeviceContext *)context->data;
	VADisplay display = ((AVVAAPIDeviceContext *)hw->hwctx)->display;
	const char *vendor = vaQueryVendorString(display);
	if (vendor != NULL) {
		g_strlcpy(out->driver, vendor, sizeof out->driver);
	}
	out->vaapi[OSS_VIDEO_HEVC] = vaapi_has_profile(display, VAProfileHEVCMain);
	out->vaapi[OSS_VIDEO_H264] =
		vaapi_has_profile(display, VAProfileH264High) || vaapi_has_profile(display, VAProfileH264Main);
	if (!out->vaapi[OSS_VIDEO_HEVC] && !out->vaapi[OSS_VIDEO_H264]) {
		g_strlcpy(out->error, "VA-API has no HEVC or H.264 decoder", sizeof out->error);
	}
	av_buffer_unref(&context);
}

static enum AVPixelFormat hardware_format(AVCodecContext *context, const enum AVPixelFormat *formats);

// probe_decodes decodes a stream of one keyframe with the decoder of this kind and says whether a
// picture came out.
static gboolean probe_decodes(OssDecoderKind kind, const char *device, const guint8 *stream, gsize length, enum AVPixelFormat software_format) {
	const AVCodec *codec = avcodec_find_decoder(AV_CODEC_ID_HEVC);
	if (codec == NULL || length == 0 || length > G_MAXINT - AV_INPUT_BUFFER_PADDING_SIZE) {
		return FALSE;
	}
	AVCodecContext *context = avcodec_alloc_context3(codec);
	AVBufferRef *hardware = NULL;
	AVPacket *packet = av_packet_alloc();
	AVFrame *frame = av_frame_alloc();
	gboolean decoded = FALSE;
	if (context == NULL || packet == NULL || frame == NULL) {
		goto done;
	}
	if (kind == OSS_DECODER_VAAPI) {
		if (av_hwdevice_ctx_create(&hardware, AV_HWDEVICE_TYPE_VAAPI, device, NULL, 0) < 0) {
			goto done;
		}
		context->hw_device_ctx = av_buffer_ref(hardware);
		context->get_format = hardware_format;
	} else {
		context->thread_count = 1;
	}
	if (avcodec_open2(context, codec, NULL) < 0 || av_new_packet(packet, (int)length) < 0) {
		goto done;
	}
	memcpy(packet->data, stream, length);
	if (avcodec_send_packet(context, packet) < 0) {
		goto done;
	}
	// The end of the stream makes the decoder give out what it holds back.
	avcodec_send_packet(context, NULL);
	while (avcodec_receive_frame(context, frame) >= 0) {
		decoded = decoded || (kind == OSS_DECODER_VAAPI ? frame->format == AV_PIX_FMT_VAAPI : frame->format == software_format);
		av_frame_unref(frame);
	}
done:
	av_frame_free(&frame);
	av_packet_free(&packet);
	avcodec_free_context(&context);
	av_buffer_unref(&hardware);
	return decoded;
}

void oss_video_probe_hdr(OssVideoProbe *out, const char *device, const guint8 *stream, gsize length) {
	if (device != NULL && device[0] == '\0') {
		device = NULL;
	}
	out->software_444_10 = probe_decodes(OSS_DECODER_SOFTWARE, NULL, stream, length, AV_PIX_FMT_YUV444P10LE);
	// VA-API opened in oss_video_probe when the driver told its name.
	out->vaapi_444_10 = out->driver[0] != '\0' && probe_decodes(OSS_DECODER_VAAPI, device, stream, length, AV_PIX_FMT_YUV444P10LE);
}

void oss_video_probe_444(OssVideoProbe *out, const char *device, const guint8 *stream, gsize length) {
	if (device != NULL && device[0] == '\0') {
		device = NULL;
	}
	out->software_444_8 = probe_decodes(OSS_DECODER_SOFTWARE, NULL, stream, length, AV_PIX_FMT_YUV444P);
	out->vaapi_444_8 = out->driver[0] != '\0' && probe_decodes(OSS_DECODER_VAAPI, device, stream, length, AV_PIX_FMT_YUV444P);
}

// ---- the decoder ---------------------------------------------------------

struct OssVideoDecoder {
	OssVideoCodec codec;
	OssDecoderKind kind;
	AVCodecContext *context;
	AVBufferRef *device;
	// display is the VADisplay of device, NULL for a decoder with no GPU device. The video processor
	// of a display belongs to the decoder that opened it (see vpp_release_display).
	void *display;
	AVPacket *packet;
	AVFrame *scratch;
	guint8 *sets; // the parameter sets with start codes
	gsize sets_length;
	OssColor color;
	GQueue frames;
	gboolean want_sync;
	gint64 submitted_us;
	gint64 received_us;
	gsize unit_bytes;
	int failures; // I/O errors in a row from the GPU decoder
	OssVideoStats stats;
};

// How many I/O errors in a row make the GPU decoder count as broken.
#define HARDWARE_FAILURE_LIMIT 3

// oss_hardware_failure_is_fatal says whether an error from the GPU decoder means it cannot
// work. Bad data is the Mac's fault and a keyframe fixes it. An I/O error can be one bad frame
// the driver choked on, so it has to repeat (failures counts them). Anything else (no format
// for the surface, no memory) is final.
gboolean oss_hardware_failure_is_fatal(int av_error, int *failures) {
	if (av_error == AVERROR_INVALIDDATA) {
		return FALSE;
	}
	if (av_error == AVERROR(EIO)) {
		return ++*failures >= HARDWARE_FAILURE_LIMIT;
	}
	return TRUE;
}

struct OssFrame {
	AVFrame *av;
	OssFrameInfo info;
	OssDecoderKind kind;
};

static enum AVPixelFormat hardware_format(AVCodecContext *context, const enum AVPixelFormat *formats) {
	for (const enum AVPixelFormat *format = formats; *format != AV_PIX_FMT_NONE; format++) {
		if (*format == AV_PIX_FMT_VAAPI) {
			return *format;
		}
	}
	return AV_PIX_FMT_NONE;
}

OssVideoDecoder *oss_video_open(OssVideoCodec codec, OssDecoderKind kind, const char *device, int threads, const guint8 *sets,
                                const int *set_lens, int set_count, OssColor color, char *err, int err_len) {
	enum AVCodecID id = codec == OSS_VIDEO_H264 ? AV_CODEC_ID_H264 : AV_CODEC_ID_HEVC;
	const AVCodec *av_codec = avcodec_find_decoder(id);
	if (av_codec == NULL) {
		g_snprintf(err, err_len, "libavcodec has no %s decoder", codec == OSS_VIDEO_H264 ? "H.264" : "HEVC");
		return NULL;
	}
	OssVideoDecoder *decoder = g_new0(OssVideoDecoder, 1);
	g_atomic_int_inc(&live_decoders);
	decoder->codec = codec;
	decoder->kind = kind;
	decoder->color = color;
	decoder->want_sync = TRUE;
	g_queue_init(&decoder->frames);
	decoder->context = avcodec_alloc_context3(av_codec);
	decoder->packet = av_packet_alloc();
	decoder->scratch = av_frame_alloc();
	if (decoder->context == NULL || decoder->packet == NULL || decoder->scratch == NULL) {
		g_snprintf(err, err_len, "out of memory");
		oss_video_close(decoder);
		return NULL;
	}
	decoder->context->flags |= AV_CODEC_FLAG_LOW_DELAY;
	if (kind == OSS_DECODER_VAAPI) {
		if (device != NULL && device[0] == '\0') {
			device = NULL;
		}
		int result = av_hwdevice_ctx_create(&decoder->device, AV_HWDEVICE_TYPE_VAAPI, device, NULL, 0);
		if (result < 0) {
			char text[AV_ERROR_MAX_STRING_SIZE];
			av_strerror(result, text, sizeof text);
			g_snprintf(err, err_len, "VA-API device: %s", text);
			oss_video_close(decoder);
			return NULL;
		}
		decoder->display = ((AVVAAPIDeviceContext *)((AVHWDeviceContext *)decoder->device->data)->hwctx)->display;
		decoder->context->hw_device_ctx = av_buffer_ref(decoder->device);
		decoder->context->get_format = hardware_format;
		// Surfaces GTK still holds are not given back to the decoder, so the pool needs room.
		decoder->context->extra_hw_frames = 8;
	} else {
		decoder->context->thread_count = threads > 0 ? threads : 1;
		decoder->context->thread_type = threads > 1 ? FF_THREAD_FRAME | FF_THREAD_SLICE : FF_THREAD_SLICE;
	}
	int result = avcodec_open2(decoder->context, av_codec, NULL);
	if (result < 0) {
		char text[AV_ERROR_MAX_STRING_SIZE];
		av_strerror(result, text, sizeof text);
		g_snprintf(err, err_len, "avcodec_open2: %s", text);
		oss_video_close(decoder);
		return NULL;
	}
	gsize total = 0;
	for (int i = 0; i < set_count; i++) {
		total += 4 + set_lens[i];
	}
	decoder->sets = g_malloc(MAX(total, 1));
	gsize used = 0;
	for (int i = 0; i < set_count; i++) {
		static const guint8 start[4] = {0, 0, 0, 1};
		memcpy(decoder->sets + used, start, 4);
		memcpy(decoder->sets + used + 4, sets, set_lens[i]);
		used += 4 + set_lens[i];
		sets += set_lens[i];
	}
	decoder->sets_length = total;
	return decoder;
}

static void frame_free_internal(OssFrame *frame) {
	frame_release(&frame->av);
	g_free(frame);
	g_atomic_int_dec_and_test(&live_frames);
}

void oss_frame_free(OssFrame *frame) {
	if (frame != NULL) {
		frame_free_internal(frame);
	}
}

static void vpp_release_display(void *display);

void oss_video_close(OssVideoDecoder *decoder) {
	if (decoder == NULL) {
		return;
	}
	// The video processor of a display lives and dies with the decoder that opened the display, in
	// whatever order the decoders close, and it goes before the display does (the device is freed
	// below).
	vpp_release_display(decoder->display);
	OssFrame *frame;
	while ((frame = g_queue_pop_head(&decoder->frames)) != NULL) {
		frame_free_internal(frame);
	}
	avcodec_free_context(&decoder->context);
	av_packet_free(&decoder->packet);
	av_frame_free(&decoder->scratch);
	av_buffer_unref(&decoder->device);
	g_free(decoder->sets);
	g_free(decoder);
	g_atomic_int_dec_and_test(&live_decoders);
}

OssDecoderKind oss_video_kind(const OssVideoDecoder *decoder) {
	return decoder->kind;
}

void oss_video_flush(OssVideoDecoder *decoder) {
	OssFrame *frame;
	while ((frame = g_queue_pop_head(&decoder->frames)) != NULL) {
		frame_free_internal(frame);
	}
	avcodec_flush_buffers(decoder->context);
	decoder->want_sync = TRUE;
	decoder->stats.flushes++;
}

static VADisplay hardware_display(const AVFrame *frame) {
	AVHWFramesContext *frames = (AVHWFramesContext *)frame->hw_frames_ctx->data;
	return ((AVVAAPIDeviceContext *)frames->device_ctx->hwctx)->display;
}

static VASurfaceID hardware_surface(const AVFrame *frame) {
	return (VASurfaceID)(uintptr_t)frame->data[3];
}

// Fills the frame's description and queues it. The frame is taken from scratch.
static int queue_frame(OssVideoDecoder *decoder) {
	AVFrame *scratch = decoder->scratch;
	gboolean hardware = scratch->format == AV_PIX_FMT_VAAPI;
	if (hardware) {
		// The GPU may still be decoding: wait now, so the time is the decode time and the
		// thread that presents never waits.
		vaSyncSurface(hardware_display(scratch), hardware_surface(scratch));
	}
	OssFrame *frame = g_new0(OssFrame, 1);
	g_atomic_int_inc(&live_frames);
	frame->av = frame_alloc();
	if (frame->av == NULL) {
		frame_free_internal(frame);
		av_frame_unref(scratch);
		return AVERROR(ENOMEM);
	}
	av_frame_move_ref(frame->av, scratch);
	frame->kind = decoder->kind;
	OssFrameInfo *info = &frame->info;
	info->hardware = hardware;
	info->width = frame->av->width - frame->av->crop_left - frame->av->crop_right;
	info->height = frame->av->height - frame->av->crop_top - frame->av->crop_bottom;
	if (hardware) {
		AVHWFramesContext *frames = (AVHWFramesContext *)frame->av->hw_frames_ctx->data;
		info->tex_width = frames->width;
		info->tex_height = frames->height;
	} else {
		info->tex_width = frame->av->width;
		info->tex_height = frame->av->height;
	}
	info->received_us = decoder->received_us;
	info->unit_bytes = decoder->unit_bytes;
	info->decoded_us = g_get_monotonic_time();
	info->decode_us = (int)(info->decoded_us - decoder->submitted_us);
	info->color = decoder->color;
	if (frame->av->color_range == AVCOL_RANGE_JPEG) {
		info->color.full_range = TRUE;
	} else if (frame->av->color_range == AVCOL_RANGE_MPEG) {
		info->color.full_range = FALSE;
	}
	decoder->stats.frames++;
	decoder->stats.decode_us_total += MAX(info->decode_us, 0);
	decoder->stats.decode_us_last = info->decode_us;
	decoder->stats.decode_us_max = MAX(decoder->stats.decode_us_max, info->decode_us);
	g_queue_push_tail(&decoder->frames, frame);
	return 0;
}

// Takes every frame the decoder has ready. Returns 0, or a libav error that is not "no more".
static int drain(OssVideoDecoder *decoder) {
	for (;;) {
		int result = avcodec_receive_frame(decoder->context, decoder->scratch);
		if (result == AVERROR(EAGAIN) || result == AVERROR_EOF) {
			return 0;
		}
		if (result < 0) {
			return result;
		}
		result = queue_frame(decoder);
		if (result < 0) {
			return result;
		}
	}
}

static gint test_fatal_kinds, test_eagain_sends;

void oss_video_test_fail_decoders(int kinds) {
	g_atomic_int_set(&test_fatal_kinds, kinds);
}

void oss_video_test_eagain(int sends) {
	g_atomic_int_set(&test_eagain_sends, sends);
}

// avcodec_send_packet, except that a test can make it answer "full" without taking the packet.
static int send_packet(OssVideoDecoder *decoder) {
	if (g_atomic_int_get(&test_eagain_sends) > 0) {
		g_atomic_int_dec_and_test(&test_eagain_sends);
		return AVERROR(EAGAIN);
	}
	return avcodec_send_packet(decoder->context, decoder->packet);
}

int oss_video_decode(OssVideoDecoder *decoder, const guint8 *data, gsize length, gboolean sync, gint64 age_us) {
	decoder->stats.units++;
	if ((g_atomic_int_get(&test_fatal_kinds) & (1 << decoder->kind)) != 0) {
		return OSS_ERR_FATAL;
	}
	if (decoder->want_sync && !sync) {
		return OSS_ERR_BAD_DATA;
	}
	gsize prefix = sync ? decoder->sets_length : 0;
	// The units are NAL units behind four byte lengths; the decoder wants start codes, which
	// are the same size, so the packet is the same size as the unit.
	if (av_new_packet(decoder->packet, (int)(prefix + length)) < 0) {
		return OSS_ERR_BAD_DATA;
	}
	guint8 *out = decoder->packet->data;
	if (prefix > 0) {
		memcpy(out, decoder->sets, prefix);
	}
	gsize offset = 0, written = prefix;
	while (offset + 4 <= length) {
		gsize size = ((gsize)data[offset] << 24) | ((gsize)data[offset + 1] << 16) | ((gsize)data[offset + 2] << 8) | data[offset + 3];
		if (size == 0 || size > length - offset - 4) {
			break;
		}
		out[written] = 0;
		out[written + 1] = 0;
		out[written + 2] = 0;
		out[written + 3] = 1;
		memcpy(out + written + 4, data + offset + 4, size);
		written += 4 + size;
		offset += 4 + size;
	}
	if (offset != length || length == 0) {
		decoder->stats.errors++;
		decoder->want_sync = TRUE;
		av_packet_unref(decoder->packet);
		return OSS_ERR_BAD_DATA;
	}
	decoder->submitted_us = g_get_monotonic_time();
	decoder->received_us = decoder->submitted_us - MAX(age_us, 0);
	decoder->unit_bytes = length;
	int result = send_packet(decoder);
	if (result == AVERROR(EAGAIN)) {
		// The decoder is full and did not take the packet: take what it has, then send the same
		// packet again. An emptied packet would tell it the stream ended.
		result = drain(decoder);
		if (result == 0) {
			result = send_packet(decoder);
		}
	}
	av_packet_unref(decoder->packet);
	if (result >= 0) {
		result = drain(decoder);
	}
	if (result < 0) {
		decoder->stats.errors++;
		decoder->want_sync = TRUE;
		avcodec_flush_buffers(decoder->context);
		if (decoder->kind == OSS_DECODER_VAAPI && oss_hardware_failure_is_fatal(result, &decoder->failures)) {
			return OSS_ERR_FATAL;
		}
		return OSS_ERR_BAD_DATA;
	}
	decoder->failures = 0;
	if (sync) {
		decoder->want_sync = FALSE;
	}
	return (int)g_queue_get_length(&decoder->frames);
}

OssFrame *oss_video_next(OssVideoDecoder *decoder) {
	return g_queue_pop_head(&decoder->frames);
}

void oss_video_stats(const OssVideoDecoder *decoder, OssVideoStats *stats) {
	*stats = decoder->stats;
}

void oss_frame_info(const OssFrame *frame, OssFrameInfo *info) {
	*info = frame->info;
}

double oss_frame_luma_mean(const OssFrame *frame) {
	if (frame->info.hardware || frame->av->data[0] == NULL) {
		return -1;
	}
	guint64 sum = 0;
	for (int y = 0; y < frame->info.height; y++) {
		const guint8 *row = frame->av->data[0] + (gsize)y * frame->av->linesize[0];
		for (int x = 0; x < frame->info.width; x++) {
			sum += row[x];
		}
	}
	return (double)sum / ((double)frame->info.width * frame->info.height);
}

// ---- color ---------------------------------------------------------------

static GMutex color_lock;
static GHashTable *color_cache;

static GdkColorState *build_cicp(int primaries, int transfer, int matrix, gboolean full) {
	GdkCicpParams *params = gdk_cicp_params_new();
	gdk_cicp_params_set_color_primaries(params, primaries);
	gdk_cicp_params_set_transfer_function(params, transfer);
	gdk_cicp_params_set_matrix_coefficients(params, matrix);
	gdk_cicp_params_set_range(params, full ? GDK_CICP_RANGE_FULL : GDK_CICP_RANGE_NARROW);
	GError *error = NULL;
	GdkColorState *state = gdk_cicp_params_build_color_state(params, &error);
	g_clear_error(&error);
	g_object_unref(params);
	return state;
}

// oss_rgb_color_state is the state of the RGB a conversion of the stream's YUV gives: the same
// primaries and transfer, full range, no matrix.
GdkColorState *oss_rgb_color_state(OssColor color) {
	int primaries = color.primaries, transfer = color.transfer;
	if (primaries == 0 || primaries == 2) {
		primaries = 1;
	}
	if (transfer == 0 || transfer == 2) {
		transfer = 13;
	}
	guint key = ((guint)primaries & 0xff) | (((guint)transfer & 0xff) << 8) | (1u << 25);
	g_mutex_lock(&color_lock);
	if (color_cache == NULL) {
		color_cache = g_hash_table_new(g_direct_hash, g_direct_equal);
	}
	GdkColorState *state = g_hash_table_lookup(color_cache, GUINT_TO_POINTER(key));
	if (state == NULL) {
		state = build_cicp(primaries, transfer, 0, TRUE);
		if (state == NULL) {
			state = gdk_color_state_get_srgb();
		}
		g_hash_table_insert(color_cache, GUINT_TO_POINTER(key), state);
	}
	g_mutex_unlock(&color_lock);
	return state;
}

GdkColorState *oss_yuv_color_state(OssColor color) {
	int primaries = color.primaries, transfer = color.transfer, matrix = color.matrix;
	if (primaries == 0 || primaries == 2 || transfer == 0 || transfer == 2) {
		return NULL;
	}
	// Matrix 0 is the identity: the three planes are G, B and R, not luma and chroma, so there is no
	// YUV state for them. Only 2 means "unspecified".
	if (matrix == 0) {
		return NULL;
	}
	if (matrix == 2) {
		matrix = primaries == 9 ? 9 : 1;
	}
	guint key = ((guint)primaries & 0xff) | (((guint)transfer & 0xff) << 8) | (((guint)matrix & 0xff) << 16) | (color.full_range ? 1u << 24 : 0) |
	            (1u << 26);
	g_mutex_lock(&color_lock);
	if (color_cache == NULL) {
		color_cache = g_hash_table_new(g_direct_hash, g_direct_equal);
	}
	GdkColorState *state = g_hash_table_lookup(color_cache, GUINT_TO_POINTER(key));
	if (state == NULL) {
		state = build_cicp(primaries, transfer, matrix, color.full_range);
		if (state != NULL) {
			g_hash_table_insert(color_cache, GUINT_TO_POINTER(key), state);
		}
	}
	g_mutex_unlock(&color_lock);
	return state;
}

GdkColorState *oss_color_state(OssColor color) {
	int primaries = color.primaries, transfer = color.transfer, matrix = color.matrix;
	// 0 and 2 are "unspecified" for the primaries and the transfer. Matrix 0 is the identity (RGB), which
	// 4:2:0 video cannot have, so a stream that says it anyway is shown as BT.709 like an unspecified one.
	if (primaries == 0 || primaries == 2) {
		primaries = 1;
	}
	if (transfer == 0 || transfer == 2) {
		transfer = 13;
	}
	if (matrix == 0 || matrix == 2) {
		matrix = 1;
	}
	guint key = ((guint)primaries & 0xff) | (((guint)transfer & 0xff) << 8) | (((guint)matrix & 0xff) << 16) | (color.full_range ? 1u << 24 : 0);
	g_mutex_lock(&color_lock);
	if (color_cache == NULL) {
		color_cache = g_hash_table_new(g_direct_hash, g_direct_equal);
	}
	GdkColorState *state = g_hash_table_lookup(color_cache, GUINT_TO_POINTER(key));
	if (state == NULL) {
		state = build_cicp(primaries, transfer, matrix, color.full_range);
		if (state == NULL) {
			// Something GTK has no name for: BT.709, as a screen is shown by default.
			state = build_cicp(1, 13, 1, color.full_range);
		}
		g_hash_table_insert(color_cache, GUINT_TO_POINTER(key), state);
	}
	g_mutex_unlock(&color_lock);
	return state;
}

gboolean oss_color_state_cicp(GdkColorState *state, int cicp[4]) {
	GdkCicpParams *params = state != NULL ? gdk_color_state_create_cicp_params(state) : NULL;
	if (params == NULL) {
		return FALSE;
	}
	cicp[0] = (int)gdk_cicp_params_get_color_primaries(params);
	cicp[1] = (int)gdk_cicp_params_get_transfer_function(params);
	cicp[2] = (int)gdk_cicp_params_get_matrix_coefficients(params);
	cicp[3] = gdk_cicp_params_get_range(params) == GDK_CICP_RANGE_FULL ? 1 : 0;
	g_object_unref(params);
	return TRUE;
}

gboolean oss_color_state_is_rec2100_pq(GdkColorState *state) {
	return state != NULL && gdk_color_state_equal(state, gdk_color_state_get_rec2100_pq());
}

// ---- textures ------------------------------------------------------------

typedef struct PoolSurface PoolSurface;
static void pool_release(PoolSurface *surface);

typedef struct {
	AVFrame *frame;
	int fds[4];
	int fd_count;
	// pooled is the output surface of the video processor the texture shows, given back when it goes.
	PoolSurface *pooled;
} DmabufHold;

static void dmabuf_release(gpointer data) {
	DmabufHold *hold = data;
	for (int i = 0; i < hold->fd_count; i++) {
		close(hold->fds[i]);
		g_atomic_int_dec_and_test(&live_fds);
	}
	frame_release(&hold->frame);
	if (hold->pooled != NULL) {
		pool_release(hold->pooled);
	}
	g_free(hold);
	g_atomic_int_dec_and_test(&live_textures);
}

static gboolean dmabuf_formats_have(GdkDisplay *display, guint32 fourcc, guint64 modifier) {
	GdkDmabufFormats *formats = gdk_display_get_dmabuf_formats(display);
	return formats != NULL && gdk_dmabuf_formats_contains(formats, fourcc, modifier);
}

gboolean oss_dmabuf_nv12_supported(GdkDisplay *display) {
	GdkDmabufFormats *formats = gdk_display_get_dmabuf_formats(display);
	if (formats == NULL) {
		return FALSE;
	}
	for (gsize i = 0; i < gdk_dmabuf_formats_get_n_formats(formats); i++) {
		guint32 fourcc;
		guint64 modifier;
		gdk_dmabuf_formats_get_format(formats, i, &fourcc, &modifier);
		if (fourcc == DRM_FORMAT_NV12) {
			return TRUE;
		}
	}
	return FALSE;
}

typedef enum {
	DMABUF_OK,
	// The display cannot take this surface (its format or modifier is not on its list, or it is
	// laid out in a way the builder has no name for). Another frame may be different.
	DMABUF_UNSUPPORTED,
	// Something that should work did not: the driver would not export, or GDK refused.
	DMABUF_FAILED,
} DmabufResult;

static gboolean test_break_dmabuf;

void oss_video_test_break_dmabuf(gboolean broken) {
	test_break_dmabuf = broken;
}

// ---- HDR video on the GPU -------------------------------------------------
//
// GDK imports no 4:4:4 10-bit picture from an Intel GPU (Y410 only with a modifier the surface does
// not have, and no 10-bit RGB with one either), and reading a 4K surface back to memory takes about
// 50 ms. The video processor of the GPU can still turn the surface into P010 (10-bit 4:2:0), which GDK
// does import, with no change of colors: the same matrix, range and transfer function in and out. It
// costs the chroma resolution, so it is used only where the picture is big (or when asked for:
// SHARELINUX_HDR_GPU=on or off). GTK's Vulkan renderer cannot sample a P010 DMA-BUF and downloads it, so
// the GL renderer is what draws it fast (see rendererForHDR).

#define HDR_GPU_PIXELS 2200000

static GMutex vpp_lock;
static struct {
	VADisplay display;
	VAConfigID config;
	VAContextID context;
	int width, height;
	gboolean ready;
} vpp;

static gint test_hdr_gpu = -1; // -1: by size, 0: never, 1: always

void oss_video_test_hdr_gpu(int mode) {
	g_atomic_int_set(&test_hdr_gpu, mode);
}

static void pool_purge_locked(void);

static void vpp_destroy_locked(void) {
	pool_purge_locked();
	if (vpp.ready) {
		vaDestroyContext(vpp.display, vpp.context);
		vaDestroyConfig(vpp.display, vpp.config);
	}
	memset(&vpp, 0, sizeof vpp);
}

// vpp_release_display lets the video processor go when it belongs to this display. It does nothing
// for a decoder without a GPU device (display NULL) or for one whose display never made the
// processor, so closing those cannot take the processor from a decoder that still uses it.
static void vpp_release_display(void *display) {
	if (display == NULL) {
		return;
	}
	g_mutex_lock(&vpp_lock);
	if (vpp.display == display) {
		vpp_destroy_locked();
	}
	g_mutex_unlock(&vpp_lock);
}

// oss_video_test_vpp_claim makes the decoder own the video processor slot without a GPU: the display
// is any pointer. oss_video_test_vpp_owner says who owns it.
void oss_video_test_vpp_claim(OssVideoDecoder *decoder, gsize display) {
	g_mutex_lock(&vpp_lock);
	decoder->display = (void *)display;
	vpp.display = (void *)display;
	g_mutex_unlock(&vpp_lock);
}

gsize oss_video_test_vpp_owner(void) {
	g_mutex_lock(&vpp_lock);
	gsize display = (gsize)vpp.display;
	g_mutex_unlock(&vpp_lock);
	return display;
}

// vpp_prepare_locked makes the video processing context for surfaces of this size on this display.
static gboolean vpp_prepare_locked(VADisplay display, int width, int height) {
	if (vpp.ready && vpp.display == display && vpp.width == width && vpp.height == height) {
		return TRUE;
	}
	vpp_destroy_locked();
	if (vaCreateConfig(display, VAProfileNone, VAEntrypointVideoProc, NULL, 0, &vpp.config) != VA_STATUS_SUCCESS) {
		return FALSE;
	}
	if (vaCreateContext(display, vpp.config, width, height, 0, NULL, 0, &vpp.context) != VA_STATUS_SUCCESS) {
		vaDestroyConfig(display, vpp.config);
		return FALSE;
	}
	vpp.display = display;
	vpp.width = width;
	vpp.height = height;
	vpp.ready = TRUE;
	return TRUE;
}

static gboolean hdr_gpu_wanted(int width, int height) {
	int mode = g_atomic_int_get(&test_hdr_gpu);
	if (mode < 0) {
		// SHARELINUX_HDR_GPU=on or off overrides the size rule.
		const char *choice = g_getenv("SHARELINUX_HDR_GPU");
		if (choice != NULL && (g_ascii_strcasecmp(choice, "on") == 0 || strcmp(choice, "1") == 0)) {
			mode = 1;
		} else if (choice != NULL && (g_ascii_strcasecmp(choice, "off") == 0 || strcmp(choice, "0") == 0)) {
			mode = 0;
		}
	}
	if (mode >= 0) {
		return mode == 1;
	}
	return (gsize)width * height > HDR_GPU_PIXELS;
}

// ---- the video processor's output surfaces ---------------------------------
//
// A new surface of 5120x1440 costs the driver about 6 ms to give out (its memory is mapped and cleared on
// first use), more than the conversion itself, so the output surfaces are kept and used again. GDK holds
// one for as long as its texture lives (a few pictures), so a few are in use at once. Each is exported to a
// DMA-BUF once; every texture gets a duplicate of the descriptors. The pool belongs to the display of the
// video processor and goes with it (vpp_destroy_locked); a surface a texture still holds at that time is
// destroyed too (the texture's own descriptors keep the buffer) and its struct is freed with the texture.

#define POOL_SIZE 8

struct PoolSurface {
	VASurfaceID id;
	int width, height;
	unsigned int rt_format;
	guint32 va_fourcc;
	VADRMPRIMESurfaceDescriptor desc;
	gboolean pooled, busy, orphan;
};

static GMutex pool_lock;
static PoolSurface *pool_surfaces[POOL_SIZE];
// pool_orphans counts the surfaces taken out of the pool that a texture still holds, for tests.
static gint pool_orphans;

static void pool_close_fds(PoolSurface *surface) {
	for (guint i = 0; i < surface->desc.num_objects && i < G_N_ELEMENTS(surface->desc.objects); i++) {
		if (surface->desc.objects[i].fd >= 0) {
			close(surface->desc.objects[i].fd);
			surface->desc.objects[i].fd = -1;
		}
	}
}

// pool_destroy_idle_locked destroys an idle surface of the pool; pool_lock and vpp_lock are held.
static void pool_destroy_idle_locked(int slot) {
	PoolSurface *surface = pool_surfaces[slot];
	vaDestroySurfaces(vpp.display, &surface->id, 1);
	pool_close_fds(surface);
	g_free(surface);
	pool_surfaces[slot] = NULL;
}

static void pool_purge_locked(void) {
	g_mutex_lock(&pool_lock);
	for (int i = 0; i < POOL_SIZE; i++) {
		PoolSurface *surface = pool_surfaces[i];
		if (surface == NULL) {
			continue;
		}
		if (!surface->busy) {
			pool_destroy_idle_locked(i);
		} else {
			// A texture still shows it. The display is still alive here, so the surface goes now; the texture's
			// own descriptors keep the buffer, and the struct is freed when the texture goes.
			vaDestroySurfaces(vpp.display, &surface->id, 1);
			surface->id = VA_INVALID_ID;
			surface->orphan = TRUE;
			g_atomic_int_inc(&pool_orphans);
			pool_close_fds(surface);
			pool_surfaces[i] = NULL;
		}
	}
	g_mutex_unlock(&pool_lock);
}

// oss_video_test_pool counts the output surfaces the pool holds, for tests.
int oss_video_test_pool(void) {
	int count = 0;
	g_mutex_lock(&pool_lock);
	for (int i = 0; i < POOL_SIZE; i++) {
		count += pool_surfaces[i] != NULL;
	}
	g_mutex_unlock(&pool_lock);
	return count;
}

// oss_video_test_pool_orphans counts the surfaces a texture holds that the pool has let go.
int oss_video_test_pool_orphans(void) {
	return g_atomic_int_get(&pool_orphans);
}

// pool_drop_format destroys the idle surfaces of a format, for one the display refused. vpp_lock is held.
static void pool_drop_format_locked(guint32 va_fourcc) {
	g_mutex_lock(&pool_lock);
	for (int i = 0; i < POOL_SIZE; i++) {
		if (pool_surfaces[i] != NULL && !pool_surfaces[i]->busy && pool_surfaces[i]->va_fourcc == va_fourcc) {
			pool_destroy_idle_locked(i);
		}
	}
	g_mutex_unlock(&pool_lock);
}

static void pool_release(PoolSurface *surface) {
	g_mutex_lock(&pool_lock);
	if (surface->orphan) {
		g_atomic_int_dec_and_test(&pool_orphans);
		g_free(surface);
	} else {
		surface->busy = FALSE;
	}
	g_mutex_unlock(&pool_lock);
}

// surface_make creates an output surface and exports it. It returns NULL when the GPU cannot.
static PoolSurface *surface_make(VADisplay display, int width, int height, unsigned int rt_format, guint32 va_fourcc) {
	VASurfaceAttrib attrib;
	memset(&attrib, 0, sizeof attrib);
	attrib.type = VASurfaceAttribPixelFormat;
	attrib.flags = VA_SURFACE_ATTRIB_SETTABLE;
	attrib.value.type = VAGenericValueTypeInteger;
	attrib.value.value.i = va_fourcc;
	PoolSurface *surface = g_new0(PoolSurface, 1);
	surface->width = width;
	surface->height = height;
	surface->rt_format = rt_format;
	surface->va_fourcc = va_fourcc;
	if (vaCreateSurfaces(display, rt_format, width, height, &surface->id, 1, &attrib, 1) != VA_STATUS_SUCCESS) {
		g_free(surface);
		return NULL;
	}
	if (vaExportSurfaceHandle(display, surface->id, VA_SURFACE_ATTRIB_MEM_TYPE_DRM_PRIME_2,
	                          VA_EXPORT_SURFACE_READ_ONLY | VA_EXPORT_SURFACE_COMPOSED_LAYERS, &surface->desc) != VA_STATUS_SUCCESS) {
		vaDestroySurfaces(display, &surface->id, 1);
		g_free(surface);
		return NULL;
	}
	return surface;
}

// pool_acquire gives an idle surface of this size and format, making one if the pool has room; vpp_lock is
// held. It returns NULL when the pool is full of surfaces in use (the caller then makes one that is not kept).
static PoolSurface *pool_acquire_locked(VADisplay display, int width, int height, unsigned int rt_format, guint32 va_fourcc) {
	g_mutex_lock(&pool_lock);
	PoolSurface *found = NULL;
	int free_slot = -1;
	for (int i = 0; i < POOL_SIZE; i++) {
		PoolSurface *surface = pool_surfaces[i];
		if (surface != NULL && !surface->busy && (surface->width != width || surface->height != height)) {
			// The picture changed size: the old surfaces are of no use.
			pool_destroy_idle_locked(i);
			surface = NULL;
		}
		if (surface == NULL) {
			free_slot = free_slot < 0 ? i : free_slot;
		} else if (found == NULL && !surface->busy && surface->rt_format == rt_format && surface->va_fourcc == va_fourcc) {
			found = surface;
		}
	}
	if (found == NULL && free_slot < 0) {
		// Full of surfaces of other formats: an idle one makes room.
		for (int i = 0; i < POOL_SIZE && free_slot < 0; i++) {
			if (!pool_surfaces[i]->busy) {
				pool_destroy_idle_locked(i);
				free_slot = i;
			}
		}
	}
	if (found == NULL && free_slot >= 0) {
		found = surface_make(display, width, height, rt_format, va_fourcc);
		if (found != NULL) {
			found->pooled = TRUE;
			pool_surfaces[free_slot] = found;
		}
	}
	if (found != NULL) {
		found->busy = TRUE;
	}
	g_mutex_unlock(&pool_lock);
	return found;
}

// vpp_convert has the GPU's video processor make a surface of another pixel format from a 4:4:4
// surface, of the same size and the same colors. For a YUV output (P010) the matrix, range and transfer
// function stay what they were; for an RGB output the processor applies the matrix, and the range comes out
// full. The output names the same color standard as the input, so the processor has no reason to move the
// primaries: the texture is told the stream's own. It fills descriptor with the output, which the caller owns
// (duplicates of the pool's descriptors, taken while the lock is held, so no other session can close them
// first), and returns TRUE; pooled is set to the pool's surface to hand to the texture, which gives it back
// with pool_release when it goes, or NULL for a surface that was not kept. It returns FALSE when the GPU cannot.
static gboolean vpp_convert(VADisplay display, VASurfaceID input, int width, int height, OssColor color, unsigned int rt_format,
                            guint32 va_fourcc, gboolean rgb, VADRMPRIMESurfaceDescriptor *descriptor, PoolSurface **pooled) {
	PoolSurface *output = NULL;
	*pooled = NULL;
	g_mutex_lock(&vpp_lock);
	if (vpp_prepare_locked(display, width, height)) {
		output = pool_acquire_locked(display, width, height, rt_format, va_fourcc);
		if (output == NULL) {
			output = surface_make(display, width, height, rt_format, va_fourcc);
		}
	}
	if (output != NULL) {
		VAProcPipelineParameterBuffer params;
		memset(&params, 0, sizeof params);
		params.surface = input;
		int standard = color.matrix == 9 || color.matrix == 10 ? VAProcColorStandardBT2020
		               : color.matrix == 5 || color.matrix == 6 ? VAProcColorStandardBT601
		                                                        : VAProcColorStandardBT709;
		params.surface_color_standard = standard;
		params.output_color_standard = standard;
		params.input_color_properties.color_range = color.full_range ? VA_SOURCE_RANGE_FULL : VA_SOURCE_RANGE_REDUCED;
		params.output_color_properties.color_range = rgb ? VA_SOURCE_RANGE_FULL : params.input_color_properties.color_range;
		VABufferID buffer = VA_INVALID_ID;
		gboolean done = FALSE;
		if (vaCreateBuffer(display, vpp.context, VAProcPipelineParameterBufferType, sizeof params, 1, &params, &buffer) == VA_STATUS_SUCCESS) {
			done = vaBeginPicture(display, vpp.context, output->id) == VA_STATUS_SUCCESS;
			if (done) {
				done = vaRenderPicture(display, vpp.context, &buffer, 1) == VA_STATUS_SUCCESS;
				done = vaEndPicture(display, vpp.context) == VA_STATUS_SUCCESS && done;
			}
			vaDestroyBuffer(display, buffer);
		}
		if (done) {
			done = vaSyncSurface(display, output->id) == VA_STATUS_SUCCESS;
		}
		if (!done) {
			if (output->pooled) {
				// Back to the pool, which may use it again for the next picture.
				pool_release(output);
			} else {
				vaDestroySurfaces(display, &output->id, 1);
				pool_close_fds(output);
				g_free(output);
			}
			output = NULL;
		} else if (output->pooled) {
			// The pool keeps its descriptors: the texture gets duplicates, and the surface goes back when it is done.
			*descriptor = output->desc;
			guint count = MIN(descriptor->num_objects, (guint)G_N_ELEMENTS(descriptor->objects));
			for (guint i = 0; i < count && done; i++) {
				descriptor->objects[i].fd = dup(output->desc.objects[i].fd);
				if (descriptor->objects[i].fd < 0) {
					for (guint j = 0; j < i; j++) {
						close(descriptor->objects[j].fd);
					}
					done = FALSE;
				}
			}
			if (done) {
				*pooled = output;
			} else {
				pool_release(output);
				output = NULL;
			}
		} else {
			// The buffer lives on in the descriptors, which the caller takes; the surface object is no longer needed.
			vaDestroySurfaces(display, &output->id, 1);
			*descriptor = output->desc;
			g_free(output);
		}
	}
	g_mutex_unlock(&vpp_lock);
	return output != NULL;
}

#define DRM_FORMAT_P010 FOURCC('P', '0', '1', '0')

// dmabuf_color_state is the state for the planes of a format GDK is given: NULL for one it is not.
static GdkColorState *dmabuf_color_state(guint32 fourcc, OssColor color) {
	switch (fourcc) {
	case DRM_FORMAT_NV12:
		return oss_color_state(color);
	case DRM_FORMAT_Y410:
	case DRM_FORMAT_P010:
		return oss_yuv_color_state(color);
	case DRM_FORMAT_XRGB8888:
	case DRM_FORMAT_XRGB2101010:
	case DRM_FORMAT_ARGB2101010:
		return oss_rgb_color_state(color);
	}
	return NULL;
}

static int dmabuf_planes(guint32 fourcc) {
	switch (fourcc) {
	case DRM_FORMAT_NV12:
	case DRM_FORMAT_P010:
		return 2;
	case DRM_FORMAT_Y410:
	case DRM_FORMAT_XRGB8888:
	case DRM_FORMAT_XRGB2101010:
	case DRM_FORMAT_ARGB2101010:
		return 1;
	}
	return 0;
}

// dmabuf_import hands an exported surface to GDK. It takes the descriptor's file descriptors whatever
// happens. keep, if given, is the decoded frame the texture must keep alive, and pooled the output surface
// of the video processor the texture shows, which is given back when the texture goes.
static DmabufResult dmabuf_import(GdkDisplay *display, VADRMPRIMESurfaceDescriptor *descriptor, const AVFrame *keep, OssColor color,
                                  PoolSurface *pooled, GdkTexture **texture) {
	DmabufHold *hold = g_new0(DmabufHold, 1);
	hold->pooled = pooled;
	hold->fd_count = (int)MIN(descriptor->num_objects, 4u);
	for (int i = 0; i < hold->fd_count; i++) {
		hold->fds[i] = descriptor->objects[i].fd;
		g_atomic_int_inc(&live_fds);
	}
	for (guint i = hold->fd_count; i < descriptor->num_objects; i++) {
		close(descriptor->objects[i].fd);
	}
	if (keep != NULL) {
		hold->frame = frame_ref(keep);
	}
	g_atomic_int_inc(&live_textures);
	DmabufResult result = DMABUF_UNSUPPORTED;
	guint32 fourcc = descriptor->num_layers == 1 ? descriptor->layers[0].drm_format : 0;
	int planes = dmabuf_planes(fourcc);
	GdkColorState *state = NULL;
	if (planes != 0 && (guint)planes == descriptor->layers[0].num_planes) {
		state = dmabuf_color_state(fourcc, color);
	} else if (descriptor->num_layers == 1) {
		g_debug("dmabuf surface: no import for format %.4s with %u planes", (const char *)&fourcc, descriptor->layers[0].num_planes);
	}
	if ((keep == NULL || hold->frame != NULL) && state != NULL) {
		const typeof(descriptor->layers[0]) *layer = &descriptor->layers[0];
		guint64 modifier = descriptor->objects[layer->object_index[0]].drm_format_modifier;
		if (dmabuf_formats_have(display, fourcc, modifier)) {
			GdkDmabufTextureBuilder *builder = gdk_dmabuf_texture_builder_new();
			gdk_dmabuf_texture_builder_set_display(builder, display);
			gdk_dmabuf_texture_builder_set_fourcc(builder, test_break_dmabuf ? FOURCC('X', 'X', 'X', 'X') : fourcc);
			gdk_dmabuf_texture_builder_set_modifier(builder, modifier);
			gdk_dmabuf_texture_builder_set_width(builder, descriptor->width);
			gdk_dmabuf_texture_builder_set_height(builder, descriptor->height);
			gdk_dmabuf_texture_builder_set_n_planes(builder, planes);
			for (int plane = 0; plane < planes; plane++) {
				gdk_dmabuf_texture_builder_set_fd(builder, plane, descriptor->objects[layer->object_index[plane]].fd);
				gdk_dmabuf_texture_builder_set_offset(builder, plane, layer->offset[plane]);
				gdk_dmabuf_texture_builder_set_stride(builder, plane, layer->pitch[plane]);
			}
			gdk_dmabuf_texture_builder_set_color_state(builder, state);
			GError *error = NULL;
			*texture = gdk_dmabuf_texture_builder_build(builder, dmabuf_release, hold, &error);
			if (*texture != NULL) {
				hold = NULL;
				result = DMABUF_OK;
			} else {
				g_debug("dmabuf texture: %s", error != NULL ? error->message : "failed");
				result = DMABUF_FAILED;
			}
			g_clear_error(&error);
			g_object_unref(builder);
		} else {
			g_debug("dmabuf surface: the display has no %.4s with modifier %lx", (const char *)&fourcc, (unsigned long)modifier);
		}
	}
	if (hold != NULL) {
		// GDK did not take it (and, if it had, the notify would have run by now).
		dmabuf_release(hold);
	}
	return result;
}

// import_converted has the GPU make a surface of another format from the decoded one and hands it to GDK.
// converted, if given, says whether the processor made the surface: UNSUPPORTED without it is a processor
// that could not (a context or surface that failed, which may pass), with it the display's own refusal.
static DmabufResult import_converted(const OssFrame *frame, GdkDisplay *display, VADisplay va, int width, int height, unsigned int rt_format,
                                     guint32 va_fourcc, gboolean rgb, GdkTexture **texture, gboolean *converted) {
	VADRMPRIMESurfaceDescriptor descriptor;
	PoolSurface *pooled = NULL;
	memset(&descriptor, 0, sizeof descriptor);
	if (converted != NULL) {
		*converted = FALSE;
	}
	if (!vpp_convert(va, hardware_surface(frame->av), width, height, frame->info.color, rt_format, va_fourcc, rgb, &descriptor, &pooled)) {
		return DMABUF_UNSUPPORTED;
	}
	if (converted != NULL) {
		*converted = TRUE;
	}
	return dmabuf_import(display, &descriptor, NULL, frame->info.color, pooled, texture);
}

// rgb10_refused is set once GDK turned down a 10 bit RGB surface of the GPU's video processor.
static gint rgb10_refused;

// rgb10_refusal says whether a failed 10 bit RGB picture means the display turns the format down for good:
// only when the processor made the surface and GDK still had no import for it. A processor that could not
// make it (the context or the surface failed) may manage the next picture, so that one only falls back.
static gboolean rgb10_refusal(gboolean converted, DmabufResult result) {
	return converted && result == DMABUF_UNSUPPORTED;
}

// rgb10_note remembers a refusal and drops the pooled 10 bit surfaces, which nothing will use again.
static void rgb10_note(gboolean converted, DmabufResult result) {
	if (!rgb10_refusal(converted, result)) {
		return;
	}
	// The processor writes linear surfaces, and Mesa's GL offers no linear 10 bit RGB to GDK: do not
	// make the conversion again for every picture.
	g_atomic_int_set(&rgb10_refused, 1);
	g_mutex_lock(&vpp_lock);
	pool_drop_format_locked(VA_FOURCC_A2R10G10B10);
	g_mutex_unlock(&vpp_lock);
}

// oss_video_test_rgb10 feeds one outcome of a 10 bit RGB picture to the refusal rule, for tests, and says
// whether the process now refuses the format. reset clears it first.
gboolean oss_video_test_rgb10(gboolean reset, gboolean converted, int result) {
	if (reset) {
		g_atomic_int_set(&rgb10_refused, 0);
	}
	rgb10_note(converted, (DmabufResult)result);
	return g_atomic_int_get(&rgb10_refused) != 0;
}

static DmabufResult texture_from_surface(const OssFrame *frame, GdkDisplay *display, GdkTexture **texture) {
	const AVFrame *av = frame->av;
	VADisplay va = hardware_display(av);
	VADRMPRIMESurfaceDescriptor descriptor;
	memset(&descriptor, 0, sizeof descriptor);
	if (vaExportSurfaceHandle(va, hardware_surface(av), VA_SURFACE_ATTRIB_MEM_TYPE_DRM_PRIME_2,
	                          VA_EXPORT_SURFACE_READ_ONLY | VA_EXPORT_SURFACE_COMPOSED_LAYERS, &descriptor) != VA_STATUS_SUCCESS) {
		return DMABUF_FAILED;
	}
	guint32 fourcc = descriptor.num_layers == 1 ? descriptor.layers[0].drm_format : 0;
	int width = descriptor.width, height = descriptor.height;
	DmabufResult result = dmabuf_import(display, &descriptor, av, frame->info.color, NULL, texture);
	if (result != DMABUF_UNSUPPORTED) {
		return result;
	}
	// GDK has no import for a 4:4:4 surface itself: the GPU makes an RGB one of it, which keeps every
	// chroma sample. HDR 4:4:4 10-bit goes to 10 bit RGB, standard range 4:4:4 8-bit to 8 bit RGB.
	if (fourcc == DRM_FORMAT_AYUV || fourcc == DRM_FORMAT_XYUV8888) {
		return import_converted(frame, display, va, width, height, VA_RT_FORMAT_RGB32, VA_FOURCC_BGRX, TRUE, texture, NULL);
	}
	if (fourcc != DRM_FORMAT_Y410 || !hdr_gpu_wanted(width, height)) {
		return result;
	}
	if (!g_atomic_int_get(&rgb10_refused)) {
		gboolean converted = FALSE;
		result = import_converted(frame, display, va, width, height, VA_RT_FORMAT_RGB32_10, VA_FOURCC_A2R10G10B10, TRUE, texture, &converted);
		if (result == DMABUF_OK) {
			return result;
		}
		rgb10_note(converted, result);
	}
	// Without 10 bit RGB the processor still makes P010, which has half the chroma resolution.
	return import_converted(frame, display, va, width, height, VA_RT_FORMAT_YUV420_10, VA_FOURCC_P010, FALSE, texture, NULL);
}

typedef struct {
	GBytes *bytes;
	GdkMemoryFormat format;
	gboolean yuv;
	// state is the color state of the pixels when it is not the SDR one of the stream's colors.
	GdkColorState *state;
	int planes;
	gsize offsets[3], strides[3];
} Pixels;

static void plane_copy(guint8 *to, gsize to_stride, const guint8 *from, int from_stride, int row_bytes, int rows) {
	for (int row = 0; row < rows; row++) {
		memcpy(to + to_stride * row, from + (gsize)from_stride * row, row_bytes);
	}
}

static gboolean test_force_swscale;

void oss_video_test_force_swscale(gboolean forced) {
	test_force_swscale = forced;
}

// Packs a decoded software picture into one buffer of the layout GTK takes. GTK before 4.20
// has no planar YUV memory formats, so there everything goes through swscale.
#if GTK_CHECK_VERSION(4, 20, 0)
static gboolean pack_planar(const AVFrame *av, int width, int height, Pixels *pixels) {
	int chroma_w = (width + 1) / 2, chroma_h = (height + 1) / 2;
	if (width % 2 != 0 || height % 2 != 0 || test_force_swscale) {
		return FALSE;
	}
	if (av->format == AV_PIX_FMT_YUV420P || av->format == AV_PIX_FMT_YUVJ420P) {
		gsize size = (gsize)width * height + 2 * (gsize)chroma_w * chroma_h;
		guint8 *buffer = g_malloc(size);
		plane_copy(buffer, width, av->data[0] + av->crop_top * av->linesize[0] + av->crop_left, av->linesize[0], width, height);
		pixels->offsets[1] = (gsize)width * height;
		pixels->offsets[2] = pixels->offsets[1] + (gsize)chroma_w * chroma_h;
		plane_copy(buffer + pixels->offsets[1], chroma_w, av->data[1] + (av->crop_top / 2) * av->linesize[1] + av->crop_left / 2,
		           av->linesize[1], chroma_w, chroma_h);
		plane_copy(buffer + pixels->offsets[2], chroma_w, av->data[2] + (av->crop_top / 2) * av->linesize[2] + av->crop_left / 2,
		           av->linesize[2], chroma_w, chroma_h);
		pixels->bytes = g_bytes_new_take(buffer, size);
		pixels->format = GDK_MEMORY_G8_B8_R8_420;
		pixels->strides[0] = width;
		pixels->strides[1] = pixels->strides[2] = chroma_w;
		pixels->planes = 3;
		pixels->yuv = TRUE;
		return TRUE;
	}
	if (av->format == AV_PIX_FMT_NV12) {
		gsize size = (gsize)width * height + (gsize)chroma_w * 2 * chroma_h;
		guint8 *buffer = g_malloc(size);
		plane_copy(buffer, width, av->data[0] + av->crop_top * av->linesize[0] + av->crop_left, av->linesize[0], width, height);
		pixels->offsets[1] = (gsize)width * height;
		plane_copy(buffer + pixels->offsets[1], (gsize)chroma_w * 2,
		           av->data[1] + (av->crop_top / 2) * av->linesize[1] + (av->crop_left / 2) * 2, av->linesize[1], chroma_w * 2, chroma_h);
		pixels->bytes = g_bytes_new_take(buffer, size);
		pixels->format = GDK_MEMORY_G8_B8R8_420;
		pixels->strides[0] = width;
		pixels->strides[1] = (gsize)chroma_w * 2;
		pixels->planes = 2;
		pixels->yuv = TRUE;
		return TRUE;
	}
	return FALSE;
}
#else
static gboolean pack_planar(const AVFrame *av, int width, int height, Pixels *pixels) {
	return FALSE;
}
#endif


// pack_planar_444_10 packs a 4:4:4 10-bit picture (three planes of 16 bit words, or the packed XV30 a
// GPU decoder gives back) into the three planes GTK takes: luma, then the two chromas, a 16 bit
// word each with the value in its low 10 bits. GTK before 4.20 has no such format.
#if GTK_CHECK_VERSION(4, 20, 0)
static gboolean pack_planar_444_10(const AVFrame *av, int width, int height, OssColor color, Pixels *pixels) {
	if ((av->format != AV_PIX_FMT_YUV444P10LE && av->format != AV_PIX_FMT_XV30LE) || test_force_swscale || G_BYTE_ORDER != G_LITTLE_ENDIAN) {
		return FALSE;
	}
	GdkColorState *state = oss_yuv_color_state(color);
	if (state == NULL) {
		return FALSE;
	}
	gsize plane = (gsize)width * height * 2;
	guint8 *buffer = g_malloc(plane * 3);
	for (int row = 0; row < height; row++) {
		guint16 *y = (guint16 *)(buffer) + (gsize)width * row;
		guint16 *u = (guint16 *)(buffer + plane) + (gsize)width * row;
		guint16 *v = (guint16 *)(buffer + 2 * plane) + (gsize)width * row;
		if (av->format == AV_PIX_FMT_YUV444P10LE) {
			memcpy(y, av->data[0] + (gsize)(av->crop_top + row) * av->linesize[0] + (gsize)av->crop_left * 2, (gsize)width * 2);
			memcpy(u, av->data[1] + (gsize)(av->crop_top + row) * av->linesize[1] + (gsize)av->crop_left * 2, (gsize)width * 2);
			memcpy(v, av->data[2] + (gsize)(av->crop_top + row) * av->linesize[2] + (gsize)av->crop_left * 2, (gsize)width * 2);
		} else {
			const guint8 *source = av->data[0] + (gsize)(av->crop_top + row) * av->linesize[0] + (gsize)av->crop_left * 4;
			for (int x = 0; x < width; x++) {
				guint32 word = GUINT32_FROM_LE(((const guint32 *)source)[x]);
				u[x] = word & 0x3ff;
				y[x] = (word >> 10) & 0x3ff;
				v[x] = (word >> 20) & 0x3ff;
			}
		}
	}
	pixels->bytes = g_bytes_new_take(buffer, plane * 3);
	pixels->format = GDK_MEMORY_X6G10_X6B10_X6R10_444;
	pixels->strides[0] = pixels->strides[1] = pixels->strides[2] = (gsize)width * 2;
	pixels->offsets[1] = plane;
	pixels->offsets[2] = 2 * plane;
	pixels->planes = 3;
	pixels->yuv = TRUE;
	pixels->state = state;
	return TRUE;
}
#else
static gboolean pack_planar_444_10(const AVFrame *av, int width, int height, OssColor color, Pixels *pixels) {
	return FALSE;
}
#endif

static gboolean is_444_10(const AVFrame *av) {
	return av->format == AV_PIX_FMT_YUV444P10LE || av->format == AV_PIX_FMT_XV30LE;
}

// Anything else is converted by swscale: to BGRA, or for 4:4:4 10-bit to 16 bit RGBA that keeps the
// stream's primaries and transfer, so a PQ picture is still PQ.
static gboolean convert_rgb(const AVFrame *picture, int width, int height, OssColor color, Pixels *pixels) {
	gboolean deep = is_444_10(picture);
	enum AVPixelFormat out_format = deep ? AV_PIX_FMT_RGBA64LE : AV_PIX_FMT_BGRA;
	// The crop of a decoded picture only says what is visible; the planes still start at the coded corner.
	AVFrame *cropped = av_frame_clone(picture);
	if (cropped == NULL) {
		return FALSE;
	}
	if (av_frame_apply_cropping(cropped, AV_FRAME_CROP_UNALIGNED) < 0 || cropped->width < width || cropped->height < height) {
		av_frame_free(&cropped);
		return FALSE;
	}
	const AVFrame *av = cropped;
	gsize stride = (gsize)width * (deep ? 8 : 4);
	// swscale's vector code may write a few bytes past the last row when the width is not a multiple of its block.
	guint8 *buffer = g_malloc(stride * height + 64);
	struct SwsContext *sws = sws_getContext(width, height, av->format, width, height, out_format, SWS_BILINEAR, NULL, NULL, NULL);
	if (sws == NULL) {
		g_free(buffer);
		av_frame_free(&cropped);
		return FALSE;
	}
	int space = color.matrix == 9 || color.matrix == 10 || (deep && color.primaries == 9 && color.matrix == 2)
	                ? SWS_CS_BT2020
	                : (color.matrix == 6 || color.matrix == 5 ? SWS_CS_SMPTE170M : SWS_CS_ITU709);
	sws_setColorspaceDetails(sws, sws_getCoefficients(space), color.full_range ? 1 : 0, sws_getCoefficients(SWS_CS_DEFAULT), 1, 0, 1 << 16,
	                         1 << 16);
	const uint8_t *source[4] = {av->data[0], av->data[1], av->data[2], av->data[3]};
	int source_stride[4] = {av->linesize[0], av->linesize[1], av->linesize[2], av->linesize[3]};
	uint8_t *destination[4] = {buffer, NULL, NULL, NULL};
	int destination_stride[4] = {(int)stride, 0, 0, 0};
	sws_scale(sws, source, source_stride, 0, height, destination, destination_stride);
	sws_freeContext(sws);
	av_frame_free(&cropped);
	pixels->bytes = g_bytes_new_take(buffer, stride * height);
	pixels->format = deep ? GDK_MEMORY_R16G16B16A16 : GDK_MEMORY_B8G8R8X8;
	pixels->strides[0] = stride;
	pixels->planes = 1;
	pixels->yuv = FALSE;
	return TRUE;
}

static GdkTexture *texture_from_memory(const AVFrame *av, const OssFrameInfo *info) {
	Pixels pixels;
	memset(&pixels, 0, sizeof pixels);
	if (!pack_planar_444_10(av, info->width, info->height, info->color, &pixels) && !pack_planar(av, info->width, info->height, &pixels) &&
	    !convert_rgb(av, info->width, info->height, info->color, &pixels)) {
		return NULL;
	}
	GdkMemoryTextureBuilder *builder = gdk_memory_texture_builder_new();
	gdk_memory_texture_builder_set_bytes(builder, pixels.bytes);
	gdk_memory_texture_builder_set_format(builder, pixels.format);
	gdk_memory_texture_builder_set_width(builder, info->width);
	gdk_memory_texture_builder_set_height(builder, info->height);
	gdk_memory_texture_builder_set_stride(builder, pixels.strides[0]);
#if GTK_CHECK_VERSION(4, 20, 0)
	for (int plane = 1; plane < pixels.planes; plane++) {
		gdk_memory_texture_builder_set_offset(builder, plane, pixels.offsets[plane]);
		gdk_memory_texture_builder_set_stride_for_plane(builder, plane, pixels.strides[plane]);
	}
#endif
	// Planes go to GTK as YUV in the stream's color state. What swscale made is RGB in the same
	// primaries and transfer, and has to say so, or a wide gamut picture is shown as sRGB.
	GdkColorState *state = pixels.state != NULL ? pixels.state : pixels.yuv ? oss_color_state(info->color) : oss_rgb_color_state(info->color);
	gdk_memory_texture_builder_set_color_state(builder, state);
	GdkTexture *texture = gdk_memory_texture_builder_build(builder);
	g_object_unref(builder);
	g_bytes_unref(pixels.bytes);
	return texture;
}

// Consecutive DMA-BUF failures that should not have happened; after a few the GPU path is not tried
// again by whoever owns the counter (a session's screen), until it is reset.
#define DMABUF_FAILURE_LIMIT 3

GdkTexture *oss_frame_texture(const OssFrame *frame, GdkDisplay *display, gboolean dmabuf, gint *dmabuf_failures, OssOutput *output) {
	*output = OSS_OUTPUT_NONE;
	if (!frame->info.hardware) {
		GdkTexture *texture = texture_from_memory(frame->av, &frame->info);
		if (texture != NULL) {
			*output = OSS_OUTPUT_MEMORY;
		}
		return texture;
	}
	if (dmabuf && g_atomic_int_get(dmabuf_failures) < DMABUF_FAILURE_LIMIT) {
		GdkTexture *texture = NULL;
		switch (texture_from_surface(frame, display, &texture)) {
		case DMABUF_OK:
			g_atomic_int_set(dmabuf_failures, 0);
			*output = OSS_OUTPUT_DMABUF;
			return texture;
		case DMABUF_FAILED:
			g_atomic_int_inc(dmabuf_failures);
			break;
		case DMABUF_UNSUPPORTED:
			break;
		}
	}
	AVFrame *copy = av_frame_alloc();
	if (copy == NULL) {
		return NULL;
	}
	copy->format = AV_PIX_FMT_NONE;
	GdkTexture *texture = NULL;
	if (av_hwframe_transfer_data(copy, frame->av, 0) >= 0) {
		copy->crop_left = frame->av->crop_left;
		copy->crop_top = frame->av->crop_top;
		copy->crop_right = frame->av->crop_right;
		copy->crop_bottom = frame->av->crop_bottom;
		texture = texture_from_memory(copy, &frame->info);
	}
	av_frame_free(&copy);
	if (texture != NULL) {
		*output = OSS_OUTPUT_COPY;
	}
	return texture;
}

void oss_dmabuf_failures_reset(gint *dmabuf_failures) {
	g_atomic_int_set(dmabuf_failures, 0);
}

// ---- for tests -----------------------------------------------------------

// oss_frame_test_texture makes the texture of a software frame as the screen does, as if the
// picture were cropped at the left and top by (crop_left, crop_top) more than it is, and reads
// one pixel (B, G, R, A) of it. matches says whether its color state is the one for expect.
gboolean oss_frame_test_texture(OssFrame *frame, int crop_left, int crop_top, int x, int y, OssColor expect, guint8 pixel[4],
                                gboolean *matches) {
	OssOutput output;
	return oss_frame_test_texture_on(frame, crop_left, crop_top, x, y, expect, pixel, matches, FALSE, &output);
}

// oss_frame_test_texture_on is that, and with gpu set the texture is made as the screen makes it for a GPU
// frame, as a DMA-BUF when the display takes one; output says which way it went.
gboolean oss_frame_test_texture_on(OssFrame *frame, int crop_left, int crop_top, int x, int y, OssColor expect, guint8 pixel[4],
                                   gboolean *matches, gboolean gpu, OssOutput *output) {
	AVFrame *av = frame->av;
	int old_left = av->crop_left, old_top = av->crop_top;
	OssFrameInfo old = frame->info;
	av->crop_left += crop_left;
	av->crop_top += crop_top;
	frame->info.width -= crop_left;
	frame->info.height -= crop_top;
	gint failures = 0;
	GdkTexture *texture = oss_frame_texture(frame, gpu ? gdk_display_get_default() : NULL, gpu, &failures, output);
	av->crop_left = old_left;
	av->crop_top = old_top;
	gboolean ok = FALSE;
	if (texture != NULL) {
		int width = gdk_texture_get_width(texture), height = gdk_texture_get_height(texture);
		GdkColorState *rgb = oss_rgb_color_state(expect);
		if (x < width && y < height) {
			gsize stride = (gsize)width * 4;
			guint8 *data = g_malloc(stride * height);
			// The pixels in the color state of the stream's RGB: planes of YUV are converted by their
			// matrix and range, and a transfer function is not applied or undone.
			GdkTextureDownloader *downloader = gdk_texture_downloader_new(texture);
			gdk_texture_downloader_set_format(downloader, GDK_MEMORY_B8G8R8A8_PREMULTIPLIED);
			gdk_texture_downloader_set_color_state(downloader, rgb);
			gdk_texture_downloader_download_into(downloader, data, stride);
			gdk_texture_downloader_free(downloader);
			memcpy(pixel, data + stride * y + 4 * x, 4);
			g_free(data);
			ok = TRUE;
		}
		// A YUV texture keeps its matrix and range in its state: the primaries and the transfer say
		// whether it is the stream's picture.
		int got[4], want[4];
		*matches = oss_color_state_cicp(gdk_texture_get_color_state(texture), got) && oss_color_state_cicp(rgb, want) && got[0] == want[0] &&
		           got[1] == want[1];
		g_object_unref(texture);
	}
	frame->info = old;
	return ok;
}
