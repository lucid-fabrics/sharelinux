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

// ---- the decoder ---------------------------------------------------------

struct OssVideoDecoder {
	OssVideoCodec codec;
	OssDecoderKind kind;
	AVCodecContext *context;
	AVBufferRef *device;
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

void oss_video_close(OssVideoDecoder *decoder) {
	if (decoder == NULL) {
		return;
	}
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

int oss_video_decode(OssVideoDecoder *decoder, const guint8 *data, gsize length, gboolean sync, gint64 age_us) {
	decoder->stats.units++;
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
	int result = avcodec_send_packet(decoder->context, decoder->packet);
	av_packet_unref(decoder->packet);
	if (result == AVERROR(EAGAIN)) {
		result = drain(decoder);
		if (result == 0) {
			result = avcodec_send_packet(decoder->context, decoder->packet);
		}
	}
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

GdkColorState *oss_color_state(OssColor color) {
	int primaries = color.primaries, transfer = color.transfer, matrix = color.matrix;
	// 0 and 2 are "unspecified", and matrix 0 is RGB, which cannot describe a YUV picture.
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

// ---- textures ------------------------------------------------------------

typedef struct {
	AVFrame *frame;
	int fds[4];
	int fd_count;
} DmabufHold;

static void dmabuf_release(gpointer data) {
	DmabufHold *hold = data;
	for (int i = 0; i < hold->fd_count; i++) {
		close(hold->fds[i]);
		g_atomic_int_dec_and_test(&live_fds);
	}
	frame_release(&hold->frame);
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

static DmabufResult texture_from_surface(const OssFrame *frame, GdkDisplay *display, GdkTexture **texture) {
	const AVFrame *av = frame->av;
	VADisplay va = hardware_display(av);
	VADRMPRIMESurfaceDescriptor descriptor;
	memset(&descriptor, 0, sizeof descriptor);
	if (vaExportSurfaceHandle(va, hardware_surface(av), VA_SURFACE_ATTRIB_MEM_TYPE_DRM_PRIME_2,
	                          VA_EXPORT_SURFACE_READ_ONLY | VA_EXPORT_SURFACE_COMPOSED_LAYERS, &descriptor) != VA_STATUS_SUCCESS) {
		return DMABUF_FAILED;
	}
	DmabufHold *hold = g_new0(DmabufHold, 1);
	hold->fd_count = (int)MIN(descriptor.num_objects, 4u);
	for (int i = 0; i < hold->fd_count; i++) {
		hold->fds[i] = descriptor.objects[i].fd;
		g_atomic_int_inc(&live_fds);
	}
	for (guint i = hold->fd_count; i < descriptor.num_objects; i++) {
		close(descriptor.objects[i].fd);
	}
	hold->frame = frame_ref(av);
	g_atomic_int_inc(&live_textures);
	DmabufResult result = DMABUF_UNSUPPORTED;
	if (hold->frame != NULL && descriptor.num_layers == 1 && descriptor.layers[0].drm_format == DRM_FORMAT_NV12 &&
	    descriptor.layers[0].num_planes == 2) {
		const typeof(descriptor.layers[0]) *layer = &descriptor.layers[0];
		guint64 modifier = descriptor.objects[layer->object_index[0]].drm_format_modifier;
		if (dmabuf_formats_have(display, DRM_FORMAT_NV12, modifier)) {
			GdkDmabufTextureBuilder *builder = gdk_dmabuf_texture_builder_new();
			gdk_dmabuf_texture_builder_set_display(builder, display);
			gdk_dmabuf_texture_builder_set_fourcc(builder, test_break_dmabuf ? FOURCC('X', 'X', 'X', 'X') : DRM_FORMAT_NV12);
			gdk_dmabuf_texture_builder_set_modifier(builder, modifier);
			gdk_dmabuf_texture_builder_set_width(builder, descriptor.width);
			gdk_dmabuf_texture_builder_set_height(builder, descriptor.height);
			gdk_dmabuf_texture_builder_set_n_planes(builder, 2);
			for (int plane = 0; plane < 2; plane++) {
				gdk_dmabuf_texture_builder_set_fd(builder, plane, descriptor.objects[layer->object_index[plane]].fd);
				gdk_dmabuf_texture_builder_set_offset(builder, plane, layer->offset[plane]);
				gdk_dmabuf_texture_builder_set_stride(builder, plane, layer->pitch[plane]);
			}
			gdk_dmabuf_texture_builder_set_color_state(builder, oss_color_state(frame->info.color));
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
		}
	}
	if (hold != NULL) {
		// GDK did not take it (and, if it had, the notify would have run by now).
		dmabuf_release(hold);
	}
	return result;
}

typedef struct {
	GBytes *bytes;
	GdkMemoryFormat format;
	gboolean yuv;
	int planes;
	gsize offsets[3], strides[3];
} Pixels;

static void plane_copy(guint8 *to, gsize to_stride, const guint8 *from, int from_stride, int row_bytes, int rows) {
	for (int row = 0; row < rows; row++) {
		memcpy(to + to_stride * row, from + (gsize)from_stride * row, row_bytes);
	}
}

// Packs a decoded software picture into one buffer of the layout GTK takes.
static gboolean pack_planar(const AVFrame *av, int width, int height, Pixels *pixels) {
	int chroma_w = (width + 1) / 2, chroma_h = (height + 1) / 2;
	if (width % 2 != 0 || height % 2 != 0) {
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

// Anything else is converted to BGRA by swscale.
static gboolean convert_bgra(const AVFrame *av, int width, int height, OssColor color, Pixels *pixels) {
	gsize stride = (gsize)width * 4;
	guint8 *buffer = g_malloc(stride * height);
	struct SwsContext *sws = sws_getContext(width, height, av->format, width, height, AV_PIX_FMT_BGRA, SWS_BILINEAR, NULL, NULL, NULL);
	if (sws == NULL) {
		g_free(buffer);
		return FALSE;
	}
	int space = color.matrix == 6 || color.matrix == 5 ? SWS_CS_SMPTE170M : SWS_CS_ITU709;
	sws_setColorspaceDetails(sws, sws_getCoefficients(space), color.full_range ? 1 : 0, sws_getCoefficients(SWS_CS_DEFAULT), 1, 0, 1 << 16,
	                         1 << 16);
	const uint8_t *source[4] = {av->data[0], av->data[1], av->data[2], av->data[3]};
	int source_stride[4] = {av->linesize[0], av->linesize[1], av->linesize[2], av->linesize[3]};
	uint8_t *destination[4] = {buffer, NULL, NULL, NULL};
	int destination_stride[4] = {(int)stride, 0, 0, 0};
	sws_scale(sws, source, source_stride, 0, height, destination, destination_stride);
	sws_freeContext(sws);
	pixels->bytes = g_bytes_new_take(buffer, stride * height);
	pixels->format = GDK_MEMORY_B8G8R8X8;
	pixels->strides[0] = stride;
	pixels->planes = 1;
	pixels->yuv = FALSE;
	return TRUE;
}

static GdkTexture *texture_from_memory(const AVFrame *av, const OssFrameInfo *info) {
	Pixels pixels;
	memset(&pixels, 0, sizeof pixels);
	if (!pack_planar(av, info->width, info->height, &pixels) && !convert_bgra(av, info->width, info->height, info->color, &pixels)) {
		return NULL;
	}
	GdkMemoryTextureBuilder *builder = gdk_memory_texture_builder_new();
	gdk_memory_texture_builder_set_bytes(builder, pixels.bytes);
	gdk_memory_texture_builder_set_format(builder, pixels.format);
	gdk_memory_texture_builder_set_width(builder, info->width);
	gdk_memory_texture_builder_set_height(builder, info->height);
	gdk_memory_texture_builder_set_stride(builder, pixels.strides[0]);
	for (int plane = 1; plane < pixels.planes; plane++) {
		gdk_memory_texture_builder_set_offset(builder, plane, pixels.offsets[plane]);
		gdk_memory_texture_builder_set_stride_for_plane(builder, plane, pixels.strides[plane]);
	}
	if (pixels.yuv) {
		gdk_memory_texture_builder_set_color_state(builder, oss_color_state(info->color));
	}
	GdkTexture *texture = gdk_memory_texture_builder_build(builder);
	g_object_unref(builder);
	g_bytes_unref(pixels.bytes);
	return texture;
}

// Consecutive DMA-BUF failures that should not have happened; after a few the GPU path is not tried again.
static gint dmabuf_failures;
#define DMABUF_FAILURE_LIMIT 3

GdkTexture *oss_frame_texture(const OssFrame *frame, GdkDisplay *display, gboolean dmabuf, OssOutput *output) {
	*output = OSS_OUTPUT_NONE;
	if (!frame->info.hardware) {
		GdkTexture *texture = texture_from_memory(frame->av, &frame->info);
		if (texture != NULL) {
			*output = OSS_OUTPUT_MEMORY;
		}
		return texture;
	}
	if (dmabuf && g_atomic_int_get(&dmabuf_failures) < DMABUF_FAILURE_LIMIT) {
		GdkTexture *texture = NULL;
		switch (texture_from_surface(frame, display, &texture)) {
		case DMABUF_OK:
			g_atomic_int_set(&dmabuf_failures, 0);
			*output = OSS_OUTPUT_DMABUF;
			return texture;
		case DMABUF_FAILED:
			g_atomic_int_inc(&dmabuf_failures);
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
