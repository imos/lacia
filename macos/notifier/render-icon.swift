import AppKit

// Draw the app icon for the notifier from brand.png. macOS 26 puts an icon
// whose outline is not the standard rounded square on a grey plate and
// shrinks it, so the round face would look small in notification banners.
// Fill the standard outline (824 px of a 1024 px canvas) with a pale purple
// close to the artwork, and draw the round face as large as the outline
// allows. brand.png itself stays as it is for the menu bar and the web.
let source = URL(fileURLWithPath: CommandLine.arguments[1])
let destination = URL(fileURLWithPath: CommandLine.arguments[2])
guard let image = NSImage(contentsOf: source),
      let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 1024,
                                    pixelsHigh: 1024, bitsPerSample: 8,
                                    samplesPerPixel: 4, hasAlpha: true,
                                    isPlanar: false, colorSpaceName: .deviceRGB,
                                    bytesPerRow: 0, bitsPerPixel: 0),
      let context = NSGraphicsContext(bitmapImageRep: bitmap) else {
    fatalError("cannot load the artwork or create the canvas")
}
let outline = NSRect(x: 100, y: 100, width: 824, height: 824)
NSGraphicsContext.saveGraphicsState()
NSGraphicsContext.current = context
NSColor.clear.setFill()
NSRect(x: 0, y: 0, width: 1024, height: 1024).fill()
NSColor(srgbRed: 0.922, green: 0.882, blue: 0.976, alpha: 1).setFill()
NSBezierPath(roundedRect: outline, xRadius: 185, yRadius: 185).fill()
context.imageInterpolation = .high
image.draw(in: outline, from: .zero, operation: .sourceOver, fraction: 1)
NSGraphicsContext.restoreGraphicsState()
guard let png = bitmap.representation(using: .png, properties: [:]) else {
    fatalError("cannot encode the icon")
}
try png.write(to: destination)
