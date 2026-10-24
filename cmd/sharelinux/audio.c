//go:build linux

#include "audio.h"

#include <libavcodec/avcodec.h>
#include <libavutil/channel_layout.h>
#include <libavutil/error.h>
#include <libavutil/mem.h>
#include <libavutil/samplefmt.h>
#include <pulse/error.h>
#include <pulse/simple.h>
#include <string.h>

struct OssAudioDecoder {
	AVCodecContext *context;
	AVPacket *packet;
	AVFrame *frame;
	int channels;
};

gboolean oss_audio_decoder_available(void) {
	return avcodec_find_decoder(AV_CODEC_ID_AAC) != NULL;
}

OssAudioDecoder *oss_audio_open(int rate, int channels, const guint8 *config, int config_len, char *err, int err_len) {
	const AVCodec *codec = avcodec_find_decoder(AV_CODEC_ID_AAC);
	if (codec == NULL) {
		g_snprintf(err, err_len, "libavcodec has no AAC decoder");
		return NULL;
	}
	OssAudioDecoder *decoder = g_new0(OssAudioDecoder, 1);
	decoder->channels = channels;
	decoder->context = avcodec_alloc_context3(codec);
	decoder->packet = av_packet_alloc();
	decoder->frame = av_frame_alloc();
	if (decoder->context == NULL || decoder->packet == NULL || decoder->frame == NULL) {
		g_snprintf(err, err_len, "out of memory");
		oss_audio_close(decoder);
		return NULL;
	}
	decoder->context->extradata = av_mallocz(config_len + AV_INPUT_BUFFER_PADDING_SIZE);
	if (decoder->context->extradata == NULL) {
		g_snprintf(err, err_len, "out of memory");
		oss_audio_close(decoder);
		return NULL;
	}
	memcpy(decoder->context->extradata, config, config_len);
	decoder->context->extradata_size = config_len;
	decoder->context->sample_rate = rate;
	av_channel_layout_default(&decoder->context->ch_layout, channels);
	decoder->context->flags |= AV_CODEC_FLAG_LOW_DELAY;
	int result = avcodec_open2(decoder->context, codec, NULL);
	if (result < 0) {
		char text[AV_ERROR_MAX_STRING_SIZE];
		av_strerror(result, text, sizeof text);
		g_snprintf(err, err_len, "avcodec_open2: %s", text);
		oss_audio_close(decoder);
		return NULL;
	}
	return decoder;
}

static gint16 to_s16(float value) {
	if (value >= 1.0f) {
		return 32767;
	}
	if (value <= -1.0f) {
		return -32768;
	}
	return (gint16)(value * 32768.0f);
}

int oss_audio_decode(OssAudioDecoder *decoder, const guint8 *data, gsize length, gint16 *out, int max_frames) {
	if (length == 0 || length > G_MAXINT) {
		return AVERROR(EINVAL);
	}
	if (av_new_packet(decoder->packet, (int)length) < 0) {
		return AVERROR(ENOMEM);
	}
	memcpy(decoder->packet->data, data, length);
	int result = avcodec_send_packet(decoder->context, decoder->packet);
	av_packet_unref(decoder->packet);
	if (result < 0) {
		return result;
	}
	result = avcodec_receive_frame(decoder->context, decoder->frame);
	if (result < 0) {
		return result;
	}
	AVFrame *frame = decoder->frame;
	int frames = MIN(frame->nb_samples, max_frames);
	int channels = MIN(decoder->channels, frame->ch_layout.nb_channels);
	memset(out, 0, (gsize)max_frames * decoder->channels * sizeof(gint16));
	for (int c = 0; c < channels; c++) {
		switch (frame->format) {
		case AV_SAMPLE_FMT_FLTP:
			for (int i = 0; i < frames; i++) {
				out[i * decoder->channels + c] = to_s16(((const float *)frame->extended_data[c])[i]);
			}
			break;
		case AV_SAMPLE_FMT_FLT:
			for (int i = 0; i < frames; i++) {
				out[i * decoder->channels + c] = to_s16(((const float *)frame->extended_data[0])[i * frame->ch_layout.nb_channels + c]);
			}
			break;
		case AV_SAMPLE_FMT_S16P:
			for (int i = 0; i < frames; i++) {
				out[i * decoder->channels + c] = ((const gint16 *)frame->extended_data[c])[i];
			}
			break;
		case AV_SAMPLE_FMT_S16:
			for (int i = 0; i < frames; i++) {
				out[i * decoder->channels + c] = ((const gint16 *)frame->extended_data[0])[i * frame->ch_layout.nb_channels + c];
			}
			break;
		default:
			av_frame_unref(frame);
			return AVERROR(ENOSYS);
		}
	}
	// A mono frame is played on both sides.
	for (int c = channels; c < decoder->channels && channels > 0; c++) {
		for (int i = 0; i < frames; i++) {
			out[i * decoder->channels + c] = out[i * decoder->channels];
		}
	}
	av_frame_unref(frame);
	return frames;
}

void oss_audio_close(OssAudioDecoder *decoder) {
	if (decoder == NULL) {
		return;
	}
	avcodec_free_context(&decoder->context);
	av_packet_free(&decoder->packet);
	av_frame_free(&decoder->frame);
	g_free(decoder);
}

struct OssAudioOut {
	pa_simple *stream;
	int channels;
};

OssAudioOut *oss_audio_out_open(int rate, int channels, int latency_ms, const char *name, char *err, int err_len) {
	pa_sample_spec spec = {.format = PA_SAMPLE_S16NE, .rate = (uint32_t)rate, .channels = (uint8_t)channels};
	size_t frame_bytes = pa_frame_size(&spec);
	pa_buffer_attr attr;
	attr.maxlength = (uint32_t)-1;
	attr.tlength = (uint32_t)pa_usec_to_bytes((pa_usec_t)latency_ms * 1000, &spec);
	attr.prebuf = (uint32_t)-1;
	attr.minreq = (uint32_t)(frame_bytes * (rate / 100));
	attr.fragsize = (uint32_t)-1;
	int error = 0;
	pa_simple *stream = pa_simple_new(NULL, name, PA_STREAM_PLAYBACK, NULL, "Sound from the Mac", &spec, NULL, &attr, &error);
	if (stream == NULL) {
		g_snprintf(err, err_len, "sound server: %s", pa_strerror(error));
		return NULL;
	}
	OssAudioOut *out = g_new0(OssAudioOut, 1);
	out->stream = stream;
	out->channels = channels;
	return out;
}

int oss_audio_out_write(OssAudioOut *out, const gint16 *samples, gsize frames) {
	int error = 0;
	if (pa_simple_write(out->stream, samples, frames * out->channels * sizeof(gint16), &error) < 0) {
		return error != 0 ? -error : -1;
	}
	return 0;
}

void oss_audio_out_flush(OssAudioOut *out) {
	int error = 0;
	pa_simple_flush(out->stream, &error);
}

gint64 oss_audio_out_latency_us(OssAudioOut *out) {
	int error = 0;
	pa_usec_t latency = pa_simple_get_latency(out->stream, &error);
	if (latency == (pa_usec_t)-1) {
		return -1;
	}
	return (gint64)latency;
}

void oss_audio_out_close(OssAudioOut *out) {
	if (out == NULL) {
		return;
	}
	int error = 0;
	// Throw away what is left rather than wait for it to play out.
	pa_simple_flush(out->stream, &error);
	pa_simple_free(out->stream);
	g_free(out);
}
