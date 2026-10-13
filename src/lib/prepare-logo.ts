export async function prepareLogo(file: File) {
  if (
    !['image/png', 'image/jpeg'].includes(file.type) ||
    file.size > 5 * 1024 * 1024
  )
    throw new Error('请选择 5 MB 以内的 PNG 或 JPG 图片。')
  const image = await createImageBitmap(file)
  try {
    if (
      !image.width ||
      !image.height ||
      image.width > 8192 ||
      image.height > 8192
    )
      throw new Error('图片尺寸过大，请选择较小的 Logo。')
    const size = Math.min(1, 256 / Math.max(image.width, image.height))
    const canvas = document.createElement('canvas')
    canvas.width = Math.max(1, Math.round(image.width * size))
    canvas.height = Math.max(1, Math.round(image.height * size))
    const context = canvas.getContext('2d')
    if (!context) throw new Error('当前浏览器无法处理图片，请更换浏览器重试。')
    context.drawImage(image, 0, 0, canvas.width, canvas.height)
    const logo = canvas.toDataURL('image/png')
    if (logo.length > 350000)
      throw new Error('图片处理后仍然过大，请使用较简单的 Logo。')
    return logo
  } finally {
    image.close()
  }
}
