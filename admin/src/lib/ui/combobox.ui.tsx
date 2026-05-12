// Combobox is a recipe, not a single component. Example composition:
//
//   <Popover>
//     <PopoverTrigger asChild>
//       <Button variant="outline">{value ?? 'Select…'}</Button>
//     </PopoverTrigger>
//     <PopoverContent class="w-[200px] p-0">
//       <Command>
//         <CommandInput placeholder="Search…" />
//         <CommandList>
//           <CommandEmpty>No results.</CommandEmpty>
//           <CommandGroup>
//             {items.map((i) => (
//               <CommandItem key={i.value} value={i.value} onSelect={setValue}>
//                 {i.label}
//               </CommandItem>
//             ))}
//           </CommandGroup>
//         </CommandList>
//       </Command>
//     </PopoverContent>
//   </Popover>
//
// Re-export helpers for convenience.

export { Popover, PopoverTrigger, PopoverContent } from './popover.ui'
export {
  Command,
  CommandInput,
  CommandList,
  CommandEmpty,
  CommandGroup,
  CommandItem,
  CommandSeparator,
} from './command.ui'
