// In-app Play URL helpers. Direct Play containers stay on GET .../video;
// everything else is HLS (.../video/hls/index.m3u8) and needs hls.js except
// Safari's native MPEG-TS/fMP4 HLS.

export function isDirectPlayContainer(path: string): boolean {
  return /\.(mp4|m4v|webm|mov)(\?|#|$)/i.test(path);
}

export function isHlsSrc(src: string): boolean {
  return /\/hls\/index\.m3u8(\?|#|$)/i.test(src) || /\.m3u8(\?|#|$)/i.test(src);
}

export function toHlsPlaylist(videoUrl: string): string {
  const u = new URL(videoUrl, "http://sak.local");
  if (u.pathname.endsWith("/hls/index.m3u8")) {
    return `${u.pathname}${u.search}`;
  }
  const path = u.pathname.replace(/\/$/, "");
  return `${path}/hls/index.m3u8${u.search}`;
}

export function inAppVideoSrc(videoUrl: string, filePath?: string): string {
  if (filePath && isDirectPlayContainer(filePath)) return videoUrl;
  if (!filePath && isDirectPlayContainer(videoUrl)) return videoUrl;
  return toHlsPlaylist(videoUrl);
}
