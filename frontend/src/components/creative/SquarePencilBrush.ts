import { Path, PencilBrush, Point, type TEvent, type TSimplePathData } from 'fabric'

/**
 * SquarePencilBrush.ts draws square mask strokes as axis-aligned stamps along
 * Fabric pointer samples. The preview and final SVG path use the same
 * interpolated stamp centers, connecting sampled segments with filled squares.
 * This representation keeps diagonal motion from introducing stroke joins or
 * round caps into the square brush.
 *
 * Fabric's inherited incremental mouse-move path calls its static segment
 * renderer, so this subclass clears and redraws the preview after accepted
 * points. The final path uses filled polygon subpaths with empty stroke
 * styling. Preview drawing applies the canvas viewport transform before filling
 * stamps, while export produces scene-coordinate path data for Fabric's normal
 * path creation lifecycle.
 */

/**
 * SquarePencilBrush implements square mask strokes through shared preview and
 * export stamps.
 */
export class SquarePencilBrush extends PencilBrush {
  /**
   * onMouseMove accepts primary Fabric pointer samples and redraws the square
   * preview. Out-of-canvas points are ignored when configured; a full redraw
   * bypasses the inherited static segment drawing used for incremental round
   * strokes.
   */
  override onMouseMove(pointer: Point, options: TEvent): void {
    if (!this.canvas._isMainEvent(options.e)) return
    if (this.limitedToCanvasSize === true && this._isOutSideCanvas(pointer)) return
    if (this._addPoint(pointer)) {
      this.canvas.clearContext(this.canvas.contextTop)
      this._render()
    }
  }

  /**
   * stampCenters interpolates square stamp centers along a nonempty pointer path.
   * The sampling step is at least one pixel and otherwise half the brush width,
   * while a distance tolerance suppresses nearly coincident centers at segment
   * joins. Each segment receives enough subdivisions to stay within that step.
   * Preview and final path conversion both call this calculation, keeping their
   * squares tied to the same retained center positions across render
   * representations.
   */
  private stampCenters(points: Point[]): Point[] {
    /**
     * Half-width sampling overlaps neighboring stamps along the pointer path.
     */
    const step = Math.max(1, this.width / 2)
    const centers: Point[] = []
    /**
     * push adds a stamp center after comparing its distance to the previous
     * retained center. Closely spaced candidates are skipped so adjacent source
     * segments share the same sampling tolerance.
     */
    const push = (x: number, y: number) => {
      const last = centers[centers.length - 1]
      /**
       * Suppress centers closer than the sampling tolerance so adjacent source
       * segments do not repeatedly add nearly identical stamp squares.
       */
      if (!last || Math.hypot(x - last.x, y - last.y) >= step * 0.9) {
        centers.push(new Point(x, y))
      }
    }
    push(points[0].x, points[0].y)
    for (let i = 1; i < points.length; i++) {
      const from = points[i - 1]
      const to = points[i]
      const dist = Math.hypot(to.x - from.x, to.y - from.y)
      const segments = Math.max(1, Math.ceil(dist / step))
      for (let s = 1; s <= segments; s++) {
        push(from.x + ((to.x - from.x) * s) / segments, from.y + ((to.y - from.y) * s) / segments)
      }
    }
    return centers
  }

  /**
   * _render paints square stamps using the supplied context or Fabric's top
   * context. Empty point sets do nothing; the viewport transform is saved before
   * drawing and restored after the preview.
   */
  override _render(ctx: CanvasRenderingContext2D = this.canvas.contextTop): void {
    const points = this._points
    if (!points.length) return
    this._saveAndTransform(ctx)
    ctx.fillStyle = this.color
    const half = this.width / 2
    for (const center of this.stampCenters(points)) {
      ctx.fillRect(center.x - half, center.y - half, this.width, this.width)
    }
    ctx.restore()
  }

  /**
   * convertPointsToSVGPath encodes the same stamp centers used by preview
   * rendering as filled square subpaths. Empty input returns an empty path;
   * otherwise each center receives width-based left, top, right, and bottom
   * coordinates. Explicit line commands return to the starting point using
   * Fabric's simple path command representation. Because every preview rectangle
   * has a corresponding polygon, final path creation can use fill-only styling
   * without introducing independent stroke joins or end caps.
   */
  override convertPointsToSVGPath(points: Point[]): TSimplePathData {
    const path: TSimplePathData = []
    if (!points.length) return path
    const half = this.width / 2
    for (const center of this.stampCenters(points)) {
      const left = center.x - half
      const top = center.y - half
      const right = center.x + half
      const bottom = center.y + half
      path.push(
        ['M', left, top],
        ['L', right, top],
        ['L', right, bottom],
        ['L', left, bottom],
        ['L', left, top],
      )
    }
    return path
  }

  /**
   * createPath builds a Fabric path with the brush color as its fill. Its empty
   * stroke and zero stroke width keep the final result governed by the square
   * polygons emitted by conversion.
   */
  override createPath(pathData: TSimplePathData): Path {
    const path = new Path(pathData, {
      fill: this.color,
      stroke: '',
      strokeWidth: 0,
    })
    return path
  }
}
