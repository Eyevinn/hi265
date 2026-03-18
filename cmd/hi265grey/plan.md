# Simplest possible general grey IDR frame generator

I want to make a new command hi265grey that constructs a grey IDR frame given VPS, SPS, PPS.

This is meant for bootstrapping a decoder for a Gradual Decode Refresh stream that lacks IDR frames.

It should not only support 4:2:0 8-bit video as the current hi265gen, but also 4:2:2 10-bit video, and possibly 4:2:0 10-bit pixel formats.

It is fine to only make this monochronic grey image with values
128 for Y, Cb, Cr for 8-bit and 512 for 10-bit.

Thus, we may be fine with not inputing a surface to the encoder, but just some structure that provides a constant
for each pixel looked up. Similarly, we may be fine
by not having a reconstruction image for prediction (provided that the prediction is also completely grey.

From a coding point of view, it is not necessary to have optimal
compression, but a simple choic of prediction is fine.

The present hi265gen can already make a grey image.
It may be beneficial to start from that program, copy relevant
part of the code and simplify it as far as possible when
it comes to image allocation and prediction. Please consider
that possibility.

After that, similar things can hopefully be done for the
10-bit case. For a uniform surfce, loop-filter should not
be needed either.

Example 4:2:2 10-bit VPS, SPS, PPS can be found in data/vps_sps_pps_422_10bit.txt. The values are repeated, so extract one copy and use it for building a final output stream
with VPS, SPS, PPS, IDR in Annex B format.

You may use ffmpeg to debug the generated grey image.
