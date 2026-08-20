import AppKit
import ApplicationServices
import Foundation

struct Node: Codable { var role: String?; var label: String?; var identifier: String?; var x: Double?; var y: Double?; var width: Double?; var height: Double?; var clickable: Bool; var children: [Node] = [] }

func value(_ element: AXUIElement, _ key: CFString) -> Any? { var result: CFTypeRef?; AXUIElementCopyAttributeValue(element, key, &result); return result }
func string(_ element: AXUIElement, _ key: CFString) -> String? { value(element, key) as? String }
func children(_ element: AXUIElement) -> [AXUIElement] { (value(element, kAXChildrenAttribute as CFString) as? [AXUIElement]) ?? [] }
func node(_ element: AXUIElement, depth: Int = 0) -> Node { let role = string(element,kAXRoleAttribute as CFString); let label = string(element,kAXTitleAttribute as CFString) ?? string(element,kAXDescriptionAttribute as CFString); let identifier = string(element,kAXIdentifierAttribute as CFString); return Node(role:role,label:label,identifier:identifier,x:nil,y:nil,width:nil,height:nil,clickable: (role == "AXButton" || role == "AXLink"),children: depth < 12 ? children(element).map { node($0,depth:depth+1) } : []) }
guard let app = NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.iphonesimulator").first else { print("{\"error\":\"Simulator.app is not running\"}"); exit(1) }
let appElement = AXUIElementCreateApplication(app.processIdentifier)
let windows = (value(appElement,kAXWindowsAttribute as CFString) as? [AXUIElement]) ?? []
let encoder = JSONEncoder(); print(String(data: try encoder.encode(windows.first.map { node($0) } ?? Node(role:nil,label:nil,identifier:nil,x:nil,y:nil,width:nil,height:nil,clickable:false)), encoding:.utf8)!)
