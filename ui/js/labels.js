// labels.js — type icons + display names for event labels (U5/U7).
// COCO labels get icons; unknown labels fall back to a generic tag.

const LABEL_META = {
  person: { icon: "🧍", name: "Person" },
  cat: { icon: "🐈", name: "Cat" },
  dog: { icon: "🐕", name: "Dog" },
  bird: { icon: "🐦", name: "Bird" },
  horse: { icon: "🐎", name: "Horse" },
  sheep: { icon: "🐑", name: "Sheep" },
  cow: { icon: "🐄", name: "Cow" },
  car: { icon: "🚗", name: "Car" },
  truck: { icon: "🚚", name: "Truck" },
  bus: { icon: "🚌", name: "Bus" },
  motorcycle: { icon: "🏍", name: "Motorcycle" },
  bicycle: { icon: "🚲", name: "Bicycle" },
  boat: { icon: "⛵", name: "Boat" },
  train: { icon: "🚂", name: "Train" },
  motion: { icon: "⚡", name: "Motion" },
};

export function labelIcon(label) {
  return (LABEL_META[label] || {}).icon || "🏷";
}

export function labelName(label) {
  const m = LABEL_META[label];
  if (m) return m.name;
  return label ? label.charAt(0).toUpperCase() + label.slice(1) : "Event";
}

export function labelChipHTML(label) {
  const icon = labelIcon(label);
  return `${icon} ${labelName(label)}`;
}
