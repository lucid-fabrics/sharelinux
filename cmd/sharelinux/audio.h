//go:build linux

#ifndef OSS_AUDIO_H
#define OSS_AUDIO_H

#include <glib.h>

// The Mac's sound: AAC-ELD packets decoded with libavcodec and played through the sound server's
// simple API (PipeWire and PulseAudio both answer it). Nothing here touches GTK, so it runs on the
// player's own thread.

typedef struct OssAudioDecoder OssAudioDecoder;
typedef struct OssAudioOut OssAudioOut;

// oss_audio_decoder_available says whether libavcodec has the AAC decoder.
gboolean oss_audio_decoder_available(void);

// oss_audio_open makes a decoder for raw AAC packets described by the AudioSpecificConfig in config.
// It returns NULL and fills err on failure.
OssAudioDecoder *oss_audio_open(int rate, int channels, const guint8 *config, int config_len, char *err, int err_len);

// oss_audio_decode decodes one packet into interleaved 16 bit samples. It returns the frames written
// (a frame is one sample of every channel), or a negative libav error.
int oss_audio_decode(OssAudioDecoder *decoder, const guint8 *data, gsize length, gint16 *out, int max_frames);

void oss_audio_close(OssAudioDecoder *decoder);

// oss_audio_out_open opens a playback stream that keeps about latency_ms of sound buffered.
OssAudioOut *oss_audio_out_open(int rate, int channels, int latency_ms, const char *name, char *err, int err_len);

// oss_audio_out_write plays frames of interleaved 16 bit samples and returns once the server has
// room for them. It returns 0, or a negative number when the stream broke.
int oss_audio_out_write(OssAudioOut *out, const gint16 *samples, gsize frames);

// oss_audio_out_flush drops what has been written and not played.
void oss_audio_out_flush(OssAudioOut *out);

// oss_audio_out_latency_us is how long a sample written now waits before it is heard, or -1.
gint64 oss_audio_out_latency_us(OssAudioOut *out);

void oss_audio_out_close(OssAudioOut *out);

#endif
