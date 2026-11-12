import {
  Component,
  DestroyRef,
  ElementRef,
  afterNextRender,
  computed,
  inject,
  input,
  model,
  output,
  signal,
  viewChild,
} from '@angular/core';
import {
  MAX_ZOOM,
  Placed,
  View,
  clampCenter,
  fit,
  pins,
  project,
  tiles,
  worldSize,
  zoomAt,
} from './geo';
import { thumbUrl, viewUrl } from './item';

/** A pin as drawn, in pixels from the map's top left. */
interface Drawn {
  left: number;
  top: number;
  items: Placed[];
  label: string;
}

/** A pointer moves this many pixels before the map moves along. */
const DRAG = 4;
/** The size until the map has measured itself, such as in tests. */
const DEFAULT_SIZE = { width: 800, height: 480 };

/**
 * The photos on a map of OpenStreetMap, close ones together as one pin
 * with the newest photo and their number. Drag or the arrow keys move the
 * map, the wheel, a pinch, + and − zoom; a click on a pin picks its photos.
 */
@Component({
  selector: 'app-photo-map',
  templateUrl: './map.html',
  styleUrl: './map.css',
})
export class PhotoMap {
  readonly items = input.required<Placed[]>();
  /** What the map shows; null fits all photos. Kept by the parent, so going back to the map shows the same. */
  readonly view = model<View | null>(null);
  readonly picked = output<Placed[]>();

  private readonly box = viewChild.required<ElementRef<HTMLElement>>('box');
  protected readonly size = signal(DEFAULT_SIZE);
  /** True once a tile could not be loaded, such as with DATA_DEN_TILES=off. */
  protected readonly noTiles = signal(false);
  /** Photos whose thumbnail failed; they show the photo itself. */
  private readonly failedThumbs = signal(new Set<string>());

  protected readonly shown = computed<View>(() => {
    const { width, height } = this.size();
    return (
      this.view() ??
      fit(
        this.items().map((item) => project(item.lat, item.lon)),
        width,
        height,
      ) ?? { center: { x: 0.5, y: 0.5 }, zoom: 1 }
    );
  });
  protected readonly tiles = computed(() => {
    const { width, height } = this.size();
    return tiles(this.shown(), width, height);
  });
  private readonly allPins = computed(() => pins(this.items(), this.shown().zoom));
  protected readonly pins = computed<Drawn[]>(() => {
    const view = this.shown();
    const { width, height } = this.size();
    const size = worldSize(view.zoom);
    const left = view.center.x * size - width / 2;
    const top = view.center.y * size - height / 2;
    const drawn: Drawn[] = [];
    for (const pin of this.allPins()) {
      let x = pin.x - left;
      // The world repeats east and west.
      if (x < -PIN_MARGIN) x += size;
      if (x > width + PIN_MARGIN) x -= size;
      const y = pin.y - top;
      if (x < -PIN_MARGIN || x > width + PIN_MARGIN || y < -PIN_MARGIN || y > height + PIN_MARGIN) {
        continue;
      }
      const first = pin.items[0];
      const label =
        pin.items.length === 1
          ? (first.place ? first.place + ': ' : '') + first.name
          : `${pin.items.length} Fotos` + (first.place ? ' bei ' + first.place : '');
      drawn.push({ left: x, top: y, items: pin.items, label });
    }
    return drawn;
  });
  protected readonly maxZoom = MAX_ZOOM;

  /** The pointers on the map, for dragging and pinching. */
  private readonly pointers = new Map<number, { x: number; y: number }>();
  private drag: { x: number; y: number; view: View; moving: boolean } | null = null;
  private pinch: number | null = null;
  private lastWheel = 0;
  /** True when the pointer last pressed moved the map, so letting go is no click. */
  private dragged = false;

  private readonly destroyRef = inject(DestroyRef);

  constructor() {
    afterNextRender(() => {
      const box = this.box().nativeElement;
      const measure = () => {
        if (box.clientWidth && box.clientHeight) {
          this.size.set({ width: box.clientWidth, height: box.clientHeight });
        }
      };
      measure();
      if (typeof ResizeObserver !== 'undefined') {
        const observer = new ResizeObserver(measure);
        observer.observe(box);
        this.destroyRef.onDestroy(() => observer.disconnect());
      }
    });
  }

  protected zoomBy(delta: number, px = 0, py = 0): void {
    const view = this.shown();
    const zoom = Math.max(0, Math.min(MAX_ZOOM, view.zoom + delta));
    if (zoom !== view.zoom) {
      this.view.set(zoomAt(view, zoom, px, py));
    }
  }

  /** Moves the map by dx, dy pixels. */
  protected pan(dx: number, dy: number, from = this.shown()): void {
    const size = worldSize(from.zoom);
    this.view.set({
      zoom: from.zoom,
      center: clampCenter({ x: from.center.x + dx / size, y: from.center.y + dy / size }),
    });
  }

  /** Where the event is, from the map's middle. */
  private fromMiddle(event: { clientX: number; clientY: number }): { x: number; y: number } {
    const rect = this.box().nativeElement.getBoundingClientRect();
    const { width, height } = this.size();
    return { x: event.clientX - rect.left - width / 2, y: event.clientY - rect.top - height / 2 };
  }

  protected wheel(event: WheelEvent): void {
    event.preventDefault();
    // A touchpad sends many small events; one step at most every 200 ms.
    const now = event.timeStamp;
    if (event.deltaY === 0 || now - this.lastWheel < 200) {
      return;
    }
    this.lastWheel = now;
    const at = this.fromMiddle(event);
    this.zoomBy(event.deltaY < 0 ? 1 : -1, at.x, at.y);
  }

  protected down(event: PointerEvent): void {
    this.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (this.pointers.size === 1) {
      this.dragged = false;
      this.drag = { x: event.clientX, y: event.clientY, view: this.shown(), moving: false };
    } else {
      this.drag = null;
      this.dragged = true;
      this.pinch = this.spread();
    }
  }

  protected move(event: PointerEvent): void {
    if (!this.pointers.has(event.pointerId)) {
      return;
    }
    this.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (this.pinch !== null && this.pointers.size === 2) {
      const spread = this.spread();
      if (spread > this.pinch * 1.5 || spread < this.pinch / 1.5) {
        const [a, b] = [...this.pointers.values()];
        const at = this.fromMiddle({ clientX: (a.x + b.x) / 2, clientY: (a.y + b.y) / 2 });
        this.zoomBy(spread > this.pinch ? 1 : -1, at.x, at.y);
        this.pinch = spread;
      }
      return;
    }
    const drag = this.drag;
    if (!drag) {
      return;
    }
    const dx = event.clientX - drag.x;
    const dy = event.clientY - drag.y;
    if (!drag.moving && Math.hypot(dx, dy) < DRAG) {
      return;
    }
    if (!drag.moving) {
      drag.moving = true;
      this.dragged = true;
      // Only now, so a click on a pin still reaches the pin.
      this.box().nativeElement.setPointerCapture?.(event.pointerId);
    }
    this.pan(-dx, -dy, drag.view);
  }

  protected up(event: PointerEvent): void {
    this.pointers.delete(event.pointerId);
    if (this.pointers.size < 2) {
      this.pinch = null;
    }
    if (this.pointers.size === 0) {
      this.drag = null;
      // After the click that follows letting go, so the keyboard can pick again.
      setTimeout(() => (this.dragged = false));
    }
  }

  /** The distance of the first two pointers. */
  private spread(): number {
    const [a, b] = [...this.pointers.values()];
    return Math.hypot(a.x - b.x, a.y - b.y);
  }

  protected key(event: KeyboardEvent): void {
    const step = 100;
    const moves: Record<string, [number, number]> = {
      ArrowLeft: [-step, 0],
      ArrowRight: [step, 0],
      ArrowUp: [0, -step],
      ArrowDown: [0, step],
    };
    if (moves[event.key]) {
      this.pan(...moves[event.key]);
    } else if (event.key === '+' || event.key === '=') {
      this.zoomBy(1);
    } else if (event.key === '-') {
      this.zoomBy(-1);
    } else {
      return;
    }
    event.preventDefault();
  }

  /** Shows all photos again. */
  protected reset(): void {
    this.view.set(null);
  }

  protected pick(pin: Drawn): void {
    // The end of dragging the map is no click.
    if (this.dragged) {
      return;
    }
    this.picked.emit(pin.items);
  }

  protected thumb(item: Placed): string {
    return item.thumb && !this.failedThumbs().has(item.path)
      ? thumbUrl(item.path)
      : viewUrl(item.path);
  }

  protected noThumb(item: Placed): void {
    if (item.thumb && !this.failedThumbs().has(item.path)) {
      this.failedThumbs.update((failed) => new Set(failed).add(item.path));
    }
  }
}

/** Pins this far outside the map are still drawn, as they reach into it. */
const PIN_MARGIN = 40;
