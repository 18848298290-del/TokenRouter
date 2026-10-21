import { reactive } from 'vue'

// previewImages.ts decodes downloaded images, creates thumbnails, and owns the
// browser URLs displayed by batch image details. Thumbnail encoding uses the
// decoded dimensions to fit the longest edge within the preview limit while
// keeping smaller images at their source size. A decoder can use either an
// ImageBitmap or an image element backed by a temporary browser URL.
//
// Decoder resources are released after drawing, and rejected image element
// loads release their temporary URL before propagating the error. Published
// preview URLs belong to a page-local collection: callers remove an individual
// failed preview or clear the collection when changing details and unmounting.
// Fetch ordering and the decision to display original bytes after encoding
// failure remain with the page that owns those requests.

// PreviewImageSource identifies the browser image accepted by canvas drawing.
type PreviewImageSource = ImageBitmap | HTMLImageElement

// DecodedPreview pairs a canvas source with its decoder resource release operation.
interface DecodedPreview {
  image: PreviewImageSource // decoded canvas image source
  width: number // pixels
  height: number // pixels
  // close releases the decoder allocation after the caller has finished drawing.
  // ImageBitmap and image element decoding supply their own resource cleanup implementation.
  close: () => void
}

// Thumbnail dimensions and encoding quality bound the browser cache's ordinary
// image payload while the page can still display original bytes after failure.
const PREVIEW_THUMBNAIL_MAX_EDGE = 360
const PREVIEW_THUMBNAIL_QUALITY = 0.72

// createThumbnailBlob encodes downloaded image bytes as a WebP thumbnail for
// browser previews. The longest edge is capped at 360 pixels and smaller
// images retain their dimensions; rounded dimensions remain at least one
// pixel.
//
// The decoder resource is released after a successful draw and before
// asynchronous encoding begins. Decode, canvas, drawing, or encoding failures
// reject for the page to handle by displaying the original Blob. Failures
// before the close call leave decoder cleanup to the browser.
export async function createThumbnailBlob(blob: Blob): Promise<Blob> {
  const source = await loadPreviewImageSource(blob)
  const width = source.width
  const height = source.height
  // Capping the scale at one avoids enlarging small images.
  const scale = Math.min(1, PREVIEW_THUMBNAIL_MAX_EDGE / Math.max(width, height))
  const targetWidth = Math.max(1, Math.round(width * scale))
  const targetHeight = Math.max(1, Math.round(height * scale))
  const canvas = document.createElement('canvas')
  canvas.width = targetWidth
  canvas.height = targetHeight
  const ctx = canvas.getContext('2d')
  if (!ctx) throw new Error('canvas unavailable')
  ctx.drawImage(source.image, 0, 0, targetWidth, targetHeight)
  // Canvas drawing has consumed the decoded source, so release its bitmap
  // or temporary URL before waiting for the independent WebP encoding callback.
  source.close()
  return await new Promise<Blob>((resolve, reject) => {
    canvas.toBlob(
      (thumbnail) => {
        if (thumbnail) resolve(thumbnail)
        else reject(new Error('thumbnail unavailable'))
      },
      'image/webp',
      PREVIEW_THUMBNAIL_QUALITY,
    )
  })
}

// loadPreviewImageSource decodes image bytes into a canvas source with
// dimensions and a release operation. Browsers exposing createImageBitmap use
// that decoder; bitmap decoding failures propagate directly to the caller.
//
// Other browsers load an image element from a temporary Blob URL. The returned
// close operation owns that URL after successful loading, while a rejected
// load revokes it before rethrowing. The caller releases a successfully
// decoded source after drawing it to its thumbnail canvas.
async function loadPreviewImageSource(blob: Blob): Promise<DecodedPreview> {
  if ('createImageBitmap' in window) {
    const bitmap = await window.createImageBitmap(blob)
    return {
      image: bitmap,
      width: bitmap.width,
      height: bitmap.height,
      close: () => bitmap.close(),
    }
  }

  const url = URL.createObjectURL(blob)
  try {
    const image = await new Promise<HTMLImageElement>((resolve, reject) => {
      const img = new Image()
      img.onload = () => resolve(img)
      img.onerror = () => reject(new Error('image unavailable'))
      img.src = url
    })
    return {
      image,
      width: image.naturalWidth || image.width,
      height: image.naturalHeight || image.height,
      close: () => URL.revokeObjectURL(url),
    }
  } catch (error) {
    // A rejected element load transfers no source to the caller, leaving
    // this function responsible for releasing the temporary URL before propagating failure.
    URL.revokeObjectURL(url)
    throw error
  }
}

// createPreviewUrls creates the reactive collection used by one batch detail page.
// Callers release individual previews before replacing them and clear published
// URLs during detail reset or unmount; pending asynchronous requests remain
// owned by the calling page.
export function createPreviewUrls() {
  // The collection retains each allocated URL under its encoded item identity
  // until the page removes that preview or clears its displayed detail state.
  const urls = reactive<Record<string, string>>({})

  // publish allocates a browser URL and exposes it under the supplied identity.
  // Callers first remove an existing entry when replacement requires releasing
  // its previous resource, and later remove or clear each published URL.
  function publish(key: string, blob: Blob) {
    urls[key] = URL.createObjectURL(blob)
  }

  // remove revokes an existing preview URL and deletes its identity mapping.
  // Missing entries are harmless, allowing display errors and explicit detail
  // resets to use the same cleanup operation.
  function remove(key: string) {
    if (urls[key]) {
      // Revoke before deleting the mapping because it is the collection's
      // retained reference to the browser allocation that needs explicit cleanup.
      URL.revokeObjectURL(urls[key])
      delete urls[key]
    }
  }

  // clear releases all currently published previews and empties their mappings.
  // Pending downloads are outside this collection and callers continue to own
  // any subsequent publication after the clear operation completes.
  function clear() {
    // Release every currently published resource before dropping identities,
    // so detail resets and unmounts free the browser allocations already displayed.
    for (const url of Object.values(urls)) {
      if (url) URL.revokeObjectURL(url)
    }
    for (const key of Object.keys(urls)) {
      delete urls[key]
    }
  }

  return { urls, publish, remove, clear }
}
